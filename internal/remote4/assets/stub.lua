-- The Remote4 stub: a driver with no business logic that stands in for the
-- real driver on the controller during `driverforge debug`.
--
-- Globals it owns: one forwarder per handler name in REMOTE4.handlers, plus
-- OnDriverInit / OnDriverLateInit / OnDriverDestroyed, Log (so the SDK can
-- forward the stub's own logs), and Remote4 (a test seam).
--
-- REMOTE4 is a global the CLI defines ahead of this file in the bundle:
--   session, userId          topic namespace (see docs/remote4-protocol.md)
--   broker { host, port, tls, skipHostnameCheck, caPem }
--   token                    mqtt-broker JWT; refreshed via `control`
--   handlers                 handler globals the real driver defines
--   driverToken              drv_ token for the SDK, optional
--   agentDriverName          SDK agent discovery override, optional
--   stubVersion              reported in `status online`
--
-- Everything here must return to the Director immediately: the MQTT client
-- delivers on this same thread, so blocking for a reply would deadlock.

local json = require('json')
local unpack = table.unpack or unpack
local function pack(...) return { n = select('#', ...), ... } end

local cfg = REMOTE4 or error('REMOTE4 config missing: this file is meant to be bundled by driverforge debug', 0)

-- Director's constant tables (C4SystemEvents and the like) exist only here on
-- the controller. Capture every flat, data-only global before the stub or the
-- SDK defines any, so the local runtime can install the same values.
local function directorConstants()
    local skip = { REMOTE4 = true, Properties = true, PersistData = true, _G = true, arg = true }
    local function scalar(v)
        local t = type(v)
        return t == 'number' or t == 'string' or t == 'boolean'
    end
    local out = {}
    for name, v in pairs(_G) do
        if type(name) == 'string' and not skip[name] then
            if scalar(v) then
                out[name] = v
            elseif type(v) == 'table' and getmetatable(v) == nil then
                local flat, count = true, 0
                for k, x in pairs(v) do
                    count = count + 1
                    if count > 2000 or type(k) ~= 'string' or not scalar(x) then flat = false break end
                end
                if flat and count > 0 then out[name] = v end
            end
        end
    end
    return out
end
local constants = directorConstants()

local PROTOCOL_VERSION = 1
local MAX_QUEUED_CALLBACKS = 200
local RECONNECT_BASE_MS = 1000
local RECONNECT_MAX_MS = 30000
local CA_FILE_NAME = 'remote4-broker-ca.pem'

Log = Log or {}
for _, level in ipairs({ 'Trace', 'Debug', 'Info', 'Warn', 'Error' }) do
    Log[level] = Log[level] or function(_, msg) print('[remote4 ' .. level:lower() .. '] ' .. tostring(msg)) end
end

local state = {
    client = nil,
    connected = false,
    ended = false,
    token = cfg.token,
    queue = {},
    refs = {},
    nextRef = 0,
    nextCallback = 0,
    reconnectDelay = RECONNECT_BASE_MS,
    reconnectTimer = nil,
}

local function topic(name)
    return 'remote4/' .. cfg.userId .. '/' .. cfg.session .. '/' .. name
end

-- Value encoding ------------------------------------------------------------

local function ref(v)
    state.nextRef = state.nextRef + 1
    state.refs[state.nextRef] = v
    return { ['$ref'] = state.nextRef, ['$type'] = type(v) }
end

-- JSON object keys are strings, so a map keyed by anything else — GetDevices'
-- { [deviceid] = {...} } — would arrive keyed by "123" and miss every numeric
-- lookup. Such a table travels as { ["$kv"] = { {k, v}, ... } } instead.
local function hasNonStringKey(v)
    if json.arrayLength(v) then return false end
    for k in pairs(v) do
        if type(k) ~= 'string' then return true end
    end
    return false
end

local encodeValue
encodeValue = function(v, depth)
    local t = type(v)
    if t == 'nil' or t == 'boolean' or t == 'number' or t == 'string' then return v end
    if t == 'table' then
        if getmetatable(v) ~= nil then return ref(v) end
        if (depth or 0) > 32 then return ref(v) end
        if hasNonStringKey(v) then
            local pairsOut = {}
            for k, x in pairs(v) do
                pairsOut[#pairsOut + 1] = { encodeValue(k, (depth or 0) + 1), encodeValue(x, (depth or 0) + 1) }
            end
            return { ['$kv'] = pairsOut }
        end
        local out = {}
        for k, x in pairs(v) do out[k] = encodeValue(x, (depth or 0) + 1) end
        if type(v.n) == 'number' then out.n = v.n end
        return out
    end
    return ref(v) -- function, userdata, thread
end

local function encodeList(list)
    local out = { n = list.n }
    for i = 1, list.n do out[i] = encodeValue(list[i]) end
    return out
end

local publish, forward

local function functionHandle(handle)
    return function(...)
        forward('__fn', handle, ...)
    end
end

local decodeValue
decodeValue = function(v)
    if type(v) ~= 'table' then return v end
    if v['$fn'] ~= nil then return functionHandle(v['$fn']) end
    if v['$ref'] ~= nil then
        local obj = state.refs[v['$ref']]
        if obj == nil then error('unknown object reference ' .. tostring(v['$ref']), 0) end
        return obj
    end
    if v['$kv'] ~= nil then
        local out = {}
        for _, kv in ipairs(v['$kv']) do
            local k = decodeValue(kv[1])
            if k ~= nil then out[k] = decodeValue(kv[2]) end
        end
        return out
    end
    local out = {}
    for k, x in pairs(v) do
        if k ~= 'n' then out[k] = decodeValue(x) end
    end
    return out
end

local function decodeList(list, argc)
    local out = { n = argc or (list and list.n) or 0 }
    for i = 1, out.n do out[i] = decodeValue(list and list[i]) end
    return out
end

-- Publishing ----------------------------------------------------------------

local function send(name, msg)
    msg.v = PROTOCOL_VERSION
    local payload = json.encode(msg)
    local code, _, err = state.client:Publish(topic(name), payload)
    if code ~= nil and code ~= 0 then
        Log:Warn('publish to ' .. name .. ' failed: ' .. tostring(err or code))
        return false
    end
    return true
end

publish = function(name, msg)
    if state.ended then return end
    if state.connected and state.client then
        if send(name, msg) then return end
    end
    if #state.queue >= MAX_QUEUED_CALLBACKS then
        Log:Warn('callback queue full, dropping ' .. tostring(msg.name or name))
        return
    end
    state.queue[#state.queue + 1] = { name = name, msg = msg }
end

local function flushQueue()
    local queued = state.queue
    state.queue = {}
    for _, item in ipairs(queued) do
        if not send(item.name, item.msg) then
            state.queue[#state.queue + 1] = item
        end
    end
end

local function snapshot()
    local ctx = {}
    if type(Properties) == 'table' then
        local props = {}
        for k, v in pairs(Properties) do props[tostring(k)] = encodeValue(v) end
        ctx.properties = props
    end
    if type(PersistData) == 'table' then ctx.persist = encodeValue(PersistData) end
    return ctx
end

-- Forwards a Director callback (or a function-handle invocation when name is
-- "__fn" and the first vararg is the handle). Never waits for a reply.
forward = function(name, ...)
    if state.ended then return end
    state.nextCallback = state.nextCallback + 1
    local msg = { id = state.nextCallback, name = name, ctx = snapshot() }
    local args = pack(...)
    if name == '__fn' then
        msg.fn = args[1]
        args = pack(select(2, ...))
    end
    msg.args = encodeList(args)
    msg.argc = args.n
    publish('callback', msg)
end

-- Executing C4 calls ---------------------------------------------------------

local function execute(msg)
    local target, fn
    if msg.ref ~= nil then
        target = state.refs[msg.ref]
        if target == nil then return { id = msg.id, ok = false, error = 'unknown object reference ' .. tostring(msg.ref) } end
    else
        target = C4
    end
    local okIndex, member = pcall(function() return target[msg.method] end)
    if not okIndex then return { id = msg.id, ok = false, error = tostring(member) } end
    fn = member
    if type(fn) ~= 'function' and type(fn) ~= 'userdata' then
        return { id = msg.id, ok = false, error = 'no such method ' .. tostring(msg.method) }
    end
    local okArgs, args = pcall(decodeList, msg.args, msg.argc)
    if not okArgs then return { id = msg.id, ok = false, error = tostring(args) } end
    local results = pack(pcall(fn, target, unpack(args, 1, args.n)))
    if not results[1] then
        return { id = msg.id, ok = false, error = tostring(results[2]) }
    end
    local out = pack(unpack(results, 2, results.n))
    return { id = msg.id, ok = true, results = encodeList(out), argc = out.n }
end

-- Inbound messages ----------------------------------------------------------

-- A log line from the driver running on the laptop, sent to Anvil through
-- this stub's SDK, the one that has verified the agent.
local FORWARDED_LEVELS = { FATAL = true, ERROR = true, WARN = true, INFO = true, DEBUG = true, TRACE = true }

local function forwardDriverLog(msg)
    if not (Anvil and Anvil.ForwardLog and FORWARDED_LEVELS[msg.level]) then return end
    local ok, args = pcall(decodeList, msg.args, msg.argc)
    if ok then Anvil:ForwardLog(msg.level, unpack(args, 1, args.n)) end
end

-- An error from the driver on the laptop, with the structured trace captured
-- there. JSON arrays arrive carrying a count (`n`) that the SDK must not see.
local function plain(v)
    if type(v) ~= 'table' then return v end
    local out = {}
    for k, x in pairs(v) do
        if not (k == 'n' and type(x) == 'number') then out[k] = plain(x) end
    end
    return out
end

local function forwardDriverError(msg)
    if not (Anvil and Anvil.CaptureError) or msg.error == nil then return end
    -- Without a trace the SDK would capture one here, of the stub's frames.
    local trace = type(msg.trace) == 'table' and plain(msg.trace)
        or { schemaVersion = 1, captureKind = 'exception', status = 'failed', reasons = { 'capture_failed' } }
    Anvil:CaptureError(tostring(msg.error), trace, { eventName = msg.name })
end

local function handleControl(msg)
    if msg.type == 'token' and type(msg.token) == 'string' then
        state.token = msg.token
        Log:Debug('broker token refreshed')
    elseif msg.type == 'end' then
        state.ended = true
        state.queue = {}
        Log:Info('Debug session ended. Run `driverforge deploy` to restore the real driver.')
        if state.client then pcall(function() state.client:Disconnect() end) end
    elseif msg.type == 'log' then
        forwardDriverLog(msg)
    elseif msg.type == 'error' then
        forwardDriverError(msg)
    end
end

local function handleCallbackResponse(msg)
    if msg.ok then return end
    local text = 'callback failed on the developer machine: ' .. tostring(msg.error)
    Log:Error(text)
    if Anvil and Anvil.CaptureError then
        pcall(Anvil.CaptureError, Anvil, text, nil, { eventName = msg.name })
    end
end

local function handleMessage(name, msg)
    if name == 'c4_call' then
        send('c4_response', execute(msg))
    elseif name == 'control' then
        handleControl(msg)
    elseif name == 'callback_response' then
        handleCallbackResponse(msg)
    end
end

-- The C4 MQTT OnMessage callback's argument order is not pinned down by the
-- DriverWorks docs (client first? topic first?), so pick the topic and payload
-- out of whatever string arguments arrive rather than by position.
local function onMessage(...)
    local topicStr, payload
    for i = 1, select('#', ...) do
        local v = select(i, ...)
        if type(v) == 'string' then
            if topicStr == nil and v:sub(1, 8) == 'remote4/' then
                topicStr = v
            elseif payload == nil and v:sub(1, 1) == '{' then
                payload = v
            end
        end
    end
    if topicStr == nil or payload == nil then
        Log:Debug('ignoring MQTT message without topic/payload')
        return
    end
    local name = topicStr:match('/([%w_]+)$')
    local ok, msg = pcall(json.decode, payload)
    if not ok or type(msg) ~= 'table' then
        Log:Warn('bad payload on ' .. tostring(name) .. ': ' .. tostring(msg))
        return
    end
    local okHandle, err = pcall(handleMessage, name, msg)
    if not okHandle then Log:Error('handling ' .. tostring(name) .. ': ' .. tostring(err)) end
end

-- Connection ----------------------------------------------------------------

local function ensureCaFile()
    C4:FileSetDir('SANDBOX')
    if C4:FileExists(CA_FILE_NAME) then C4:FileDelete(CA_FILE_NAME) end
    local fh = C4:FileOpen(CA_FILE_NAME)
    if fh == nil or fh < 0 then return nil end
    C4:FileWrite(fh, #cfg.broker.caPem, cfg.broker.caPem)
    C4:FileClose(fh)
    return C4:GetSandboxDir() .. '/' .. CA_FILE_NAME
end

local function driverFilename()
    local ok, name = pcall(function() return C4:GetDriverFileName() end)
    if ok and type(name) == 'string' then return name end
    return nil
end

local function announce()
    send('status', { type = 'online', stub = tostring(cfg.stubVersion or 'dev'),
        driver = driverFilename(), handlers = { n = #cfg.handlers, unpack(cfg.handlers) },
        ctx = { env = constants } })
end

local scheduleReconnect

local function subscribe(client)
    for _, name in ipairs({ 'c4_call', 'control', 'callback_response' }) do
        local ok, code, _, err = pcall(function() return client:Subscribe(topic(name), 1) end)
        if not ok then ok, code = pcall(function() return client:Subscribe(topic(name)) end) end
        if not ok or (code ~= nil and code ~= 0) then
            Log:Error('subscribe ' .. name .. ' failed: ' .. tostring(err or code))
        end
    end
end

local function connect()
    local clientId = cfg.userId .. '|remote4-' .. cfg.session
    local client = C4:MQTT(clientId)
    state.client = client

    client:OnConnect(function()
        state.connected = true
        state.reconnectDelay = RECONNECT_BASE_MS
        Log:Info('connected to broker; debug session ' .. cfg.session .. ' live')
        subscribe(client)
        announce()
        flushQueue()
    end)

    client:OnDisconnect(function(_, code, message)
        state.connected = false
        if state.ended then return end
        Log:Warn('broker connection lost (' .. tostring(code) .. ' ' .. tostring(message) .. '); reconnecting')
        scheduleReconnect()
    end)

    client:OnMessage(onMessage)

    pcall(function()
        client:SetWill(topic('status'), json.encode({ v = PROTOCOL_VERSION, type = 'offline' }), 1, false)
    end)

    client:SetUsernameAndPassword('remote4', state.token)

    if cfg.broker.tls then
        local caFile = ensureCaFile()
        if caFile == nil then
            Log:Error('could not write the broker CA into the sandbox; not connecting')
            return
        end
        client:SetTlsCertificates(caFile)
        if cfg.broker.skipHostnameCheck then client:SetTlsInsecure(true) end
    end

    local code, message = client:Connect(cfg.broker.host, tonumber(cfg.broker.port) or cfg.broker.port)
    if code ~= nil and code ~= 0 then
        Log:Error('broker connect failed: ' .. tostring(message or code))
        scheduleReconnect()
    end
end

scheduleReconnect = function()
    if state.ended or state.reconnectTimer then return end
    local delay = state.reconnectDelay
    state.reconnectDelay = math.min(delay * 2, RECONNECT_MAX_MS)
    state.reconnectTimer = C4:SetTimer(delay, function()
        state.reconnectTimer = nil
        if state.ended or state.connected then return end
        state.client:SetUsernameAndPassword('remote4', state.token)
        local code, message = state.client:Reconnect()
        if code ~= nil and code ~= 0 then
            Log:Warn('reconnect failed: ' .. tostring(message or code))
            scheduleReconnect()
        end
    end)
end

-- Handler globals -----------------------------------------------------------
-- Defined before the SDK is required: the harness captures the pre-existing
-- globals by value and wraps them, so every forwarded callback is
-- instrumented as an Anvil event.

local LIFECYCLE = { OnDriverInit = true, OnDriverLateInit = true, OnDriverDestroyed = true }
local wanted = {}
for _, name in ipairs(cfg.handlers or {}) do wanted[name] = true end

for name in pairs(wanted) do
    if not LIFECYCLE[name] then
        _G[name] = function(...) return forward(name, ...) end
    end
end

function OnDriverInit(...)
    connect()
    if wanted.OnDriverInit then forward('OnDriverInit', ...) end
end

function OnDriverLateInit(...)
    if wanted.OnDriverLateInit then forward('OnDriverLateInit', ...) end
end

function OnDriverDestroyed(...)
    if wanted.OnDriverDestroyed then forward('OnDriverDestroyed', ...) end
    state.ended = true
    if state.client then pcall(function() state.client:Disconnect() end) end
end

-- Anvil instrumentation: the bundle preloads the SDK when the project has a
-- driver token; a bare stub (specs, or a project without one) runs without.
if cfg.driverToken then
    local ok, err = pcall(require, 'vendor.driverforge-sdk')
    if ok and Driverforge and Driverforge.Init then
        Driverforge:Init(cfg.driverToken, { logger = Log, agentDriverName = cfg.agentDriverName })
    else
        Log:Warn('SDK unavailable, stub runs uninstrumented: ' .. tostring(err))
    end
end

Remote4 = {
    _internals = {
        state = state, topic = topic, encodeValue = encodeValue, decodeValue = decodeValue, constants = constants,
        execute = execute, forward = forward, onMessage = onMessage, handleMessage = handleMessage,
    },
}

-- The Remote4 runtime: the plain Lua process `driverforge debug` spawns on
-- the developer's machine. It loads the driver's source, dispatches the
-- callbacks the CLI relays from the controller, and proxies every `C4` call
-- back through the CLI. Protocol: docs/remote4-protocol.md "CLI ↔ runtime".
--
--   lua runtime.lua --src <dir> [--entry driver.lua] [--config <name>]
--                   [--mobdebug host:port]
--
-- stdin  : JSON lines from the CLI (callback, c4_response, reload, shutdown)
-- stdout : JSON lines to the CLI; `print` is redirected into a message so the
--          driver's output reaches the terminal without corrupting the stream.
--
-- Runs under Lua 5.1 / LuaJIT, the same dialect as the controller.

local unpack = table.unpack or unpack
local function pack(...) return { n = select('#', ...), ... } end

local PROTOCOL_VERSION = 1

-- Arguments -----------------------------------------------------------------

local opts = { entry = 'driver.lua' }
do
    local args = arg or {}
    local i = 1
    while i <= #args do
        local a = args[i]
        if a == '--src' then opts.src = args[i + 1]; i = i + 2
        elseif a == '--entry' then opts.entry = args[i + 1]; i = i + 2
        elseif a == '--config' then opts.config = args[i + 1]; i = i + 2
        elseif a == '--mobdebug' then opts.mobdebug = args[i + 1] or 'localhost:8172'; i = i + 2
        else i = i + 1 end
    end
end

-- The codec sits beside this file; resolve it relative to the script path so
-- the runtime works from any working directory.
local here = (arg and arg[0] or 'runtime.lua'):match('^(.*)[/\\]') or '.'
package.path = here .. '/?.lua;' .. package.path
local json = require('json')

-- Transport -----------------------------------------------------------------

local stdout, stdin = io.stdout, io.stdin

local function emit(msg)
    msg.v = PROTOCOL_VERSION
    stdout:write(json.encode(msg), '\n')
    stdout:flush()
end

local function readMessage()
    while true do
        local line = stdin:read('*l')
        if line == nil then return nil end
        if line:match('^%s*{') then
            local ok, msg = pcall(json.decode, line)
            if ok and type(msg) == 'table' then return msg end
            emit({ type = 'log', level = 'warn', message = 'runtime: bad message from CLI: ' .. tostring(msg) })
        end
    end
end

local realPrint = print
function print(...)
    local parts = {}
    for i = 1, select('#', ...) do parts[i] = tostring((select(i, ...))) end
    emit({ type = 'print', text = table.concat(parts, '\t') })
end

local function fatal(err)
    emit({ type = 'fatal', error = tostring(err) })
    os.exit(1)
end

if not opts.src then fatal('runtime: --src <dir> is required') end

-- Value encoding ------------------------------------------------------------
-- Mirrors the stub's rules from the laptop side: functions become handles the
-- controller can invoke, proxies become the controller's object references.

local fnHandles = setmetatable({}, { __mode = 'k' }) -- function -> handle
local fnByHandle = {}                                  -- handle -> function
local nextFn = 0
local proxyRef = setmetatable({}, { __mode = 'k' })    -- proxy -> ref id
local proxyById = {}                                   -- ref id -> proxy

local call -- forward declaration

local function newProxy(id, typ)
    local proxy = proxyById[id]
    if proxy then return proxy end
    proxy = setmetatable({}, {
        __index = function(_, method)
            return function(_, ...) return call(id, method, ...) end
        end,
        __tostring = function() return 'remote4 ' .. tostring(typ) .. ' #' .. id end,
    })
    proxyById[id] = proxy
    proxyRef[proxy] = id
    return proxy
end

local encodeValue
encodeValue = function(v, depth)
    local t = type(v)
    if t == 'nil' or t == 'boolean' or t == 'number' or t == 'string' then return v end
    if t == 'function' then
        local handle = fnHandles[v]
        if not handle then
            nextFn = nextFn + 1
            handle = nextFn
            fnHandles[v] = handle
            fnByHandle[handle] = v
        end
        return { ['$fn'] = handle }
    end
    if t == 'table' then
        if proxyRef[v] then return { ['$ref'] = proxyRef[v] } end
        if (depth or 0) > 32 then error('remote4: value nests too deeply to send', 0) end
        local out = {}
        for k, x in pairs(v) do out[k] = encodeValue(x, (depth or 0) + 1) end
        if type(v.n) == 'number' then out.n = v.n end
        return out
    end
    error('remote4: cannot send a ' .. t .. ' to the controller', 0)
end

local function encodeList(list)
    local out = { n = list.n }
    for i = 1, list.n do out[i] = encodeValue(list[i]) end
    return out
end

local decodeValue
decodeValue = function(v)
    if type(v) ~= 'table' then return v end
    if v['$ref'] ~= nil then return newProxy(v['$ref'], v['$type']) end
    local out = {}
    for k, x in pairs(v) do
        if k ~= 'n' then out[k] = decodeValue(x) end
    end
    if type(v.n) == 'number' then out.n = v.n end
    return out
end

local function decodeList(list, argc)
    local out = { n = argc or (list and list.n) or 0 }
    for i = 1, out.n do out[i] = decodeValue(list and list[i]) end
    return out
end

-- Calls into the controller ---------------------------------------------------

local pendingCallbacks = {}
local pendingReload = false
local shuttingDown = false
local nextCall = 0

local handleControlMessage -- forward declaration (reload/shutdown)

call = function(ref, method, ...)
    nextCall = nextCall + 1
    local id = nextCall
    local args = pack(...)
    emit({ type = 'c4_call', id = id, ref = ref, method = method, args = encodeList(args), argc = args.n })
    while true do
        local msg = readMessage()
        if msg == nil then os.exit(0) end
        if msg.type == 'c4_response' and msg.id == id then
            if not msg.ok then error(tostring(msg.error), 0) end
            local results = decodeList(msg.results, msg.argc)
            return unpack(results, 1, results.n)
        elseif msg.type == 'callback' then
            pendingCallbacks[#pendingCallbacks + 1] = msg
        else
            handleControlMessage(msg)
        end
    end
end

-- The local C4 ---------------------------------------------------------------

local AGENT_DRIVER_PATTERN = '^anvil%-agent.*%.c4z$'

local function stripN(v)
    if type(v) ~= 'table' then return v end
    local out = {}
    for k, x in pairs(v) do
        if k ~= 'n' then out[k] = stripN(x) end
    end
    return out
end

Properties = Properties or {}
PersistData = PersistData or {}

C4 = setmetatable({
    JsonEncode = function(_, v) return json.encode(v) end,
    JsonDecode = function(_, s)
        local ok, v = pcall(json.decode, s)
        if not ok or v == json.null then return nil end
        return stripN(v)
    end,
    -- The developer's own vendored SDK must find no agent, so it stays inert
    -- and the stub's SDK remains the single instrumentation point.
    GetDevicesByC4iName = function(_, name)
        if type(name) == 'string' and name:match(AGENT_DRIVER_PATTERN) then return {} end
        return call(nil, 'GetDevicesByC4iName', name)
    end,
    UpdateProperty = function(_, name, value, ...)
        Properties[name] = value
        return call(nil, 'UpdateProperty', name, value, ...)
    end,
}, {
    __index = function(_, method)
        return function(_, ...) return call(nil, method, ...) end
    end,
})

-- Loading the driver ---------------------------------------------------------

local function fileExists(path)
    local f = io.open(path, 'r')
    if f then f:close() return true end
    return false
end

local projectModules = {}

-- Lua 5.1 names the searcher list package.loaders; 5.2+ package.searchers.
local searchers = package.loaders or package.searchers
table.insert(searchers, 2, function(name)
    local rel = name:gsub('%.', '/')
    for _, candidate in ipairs({ opts.src .. '/' .. rel .. '.lua', opts.src .. '/' .. rel .. '/init.lua' }) do
        if fileExists(candidate) then
            local chunk, err = loadfile(candidate)
            if not chunk then error(err, 0) end
            projectModules[name] = true
            return chunk
        end
    end
    return nil
end)

-- `driverforge build` swaps config.<configuration>.lua in as config.lua; do
-- the same here so the driver sees the configuration it was asked to debug.
local function installConfigMapping()
    package.loaded['config'] = nil
    package.preload['config'] = nil
    if not opts.config then return end
    local candidate = opts.src .. '/config.' .. opts.config .. '.lua'
    if fileExists(candidate) then
        package.preload['config'] = function(...)
            local chunk, err = loadfile(candidate)
            if not chunk then error(err, 0) end
            projectModules['config'] = true
            return chunk(...)
        end
    end
end

local function functionGlobals()
    local names = {}
    for k, v in pairs(_G) do
        if type(k) == 'string' and type(v) == 'function' then names[k] = true end
    end
    return names
end

local baseline = functionGlobals()
baseline.print = true

local function handlerNames()
    local names = {}
    for k in pairs(functionGlobals()) do
        if not baseline[k] then names[#names + 1] = k end
    end
    table.sort(names)
    return names
end

local function loadDriver()
    installConfigMapping()
    for name in pairs(projectModules) do package.loaded[name] = nil end
    projectModules = {}
    local path = opts.src .. '/' .. opts.entry
    local chunk, err = loadfile(path)
    if not chunk then return false, err end
    local ok, runErr = xpcall(chunk, debug.traceback)
    if not ok then return false, runErr end
    return true
end

if opts.mobdebug then
    local host, port = opts.mobdebug:match('^([^:]*):?(%d*)$')
    local ok, mobdebug = pcall(require, 'mobdebug')
    if ok then
        mobdebug.start(host ~= '' and host or nil, tonumber(port))
    else
        emit({ type = 'log', level = 'warn', message = 'mobdebug not installed (luarocks install mobdebug); continuing without a debugger' })
    end
end

-- Compile only, for now. Drivers routinely call C4 at file scope
-- (C4:GetDriverFileName() in a logger, say), and those calls can only be
-- answered once the stub is live on the controller — so the entry file is
-- checked for syntax here and run when the CLI sends `load`. A syntax error
-- still stops the session before the controller is touched.
do
    local path = opts.src .. '/' .. opts.entry
    local chunk, err = loadfile(path)
    if not chunk then fatal(err) end
    emit({ type = 'compiled', entry = opts.entry })
end

local loaded = false

-- Dispatch -------------------------------------------------------------------

local function installContext(ctx)
    if type(ctx) ~= 'table' then return end
    if type(ctx.properties) == 'table' then Properties = stripN(decodeValue(ctx.properties)) end
    if type(ctx.persist) == 'table' then PersistData = stripN(decodeValue(ctx.persist)) end
end

local function dispatch(msg)
    installContext(msg.ctx)
    local fn
    if msg.name == '__fn' then
        fn = fnByHandle[msg.fn]
    else
        fn = rawget(_G, msg.name)
    end
    if type(fn) ~= 'function' then
        emit({ type = 'callback_response', id = msg.id, name = msg.name, ok = true, handled = false })
        return
    end
    local args = decodeList(msg.args, msg.argc)
    local results = pack(xpcall(function() return fn(unpack(args, 1, args.n)) end, debug.traceback))
    if not results[1] then
        emit({ type = 'callback_response', id = msg.id, name = msg.name, ok = false, error = tostring(results[2]) })
        return
    end
    local out = pack(unpack(results, 2, results.n))
    local okEncode, encoded = pcall(encodeList, out)
    if not okEncode then
        emit({ type = 'callback_response', id = msg.id, name = msg.name, ok = true, handled = true, argc = 0 })
        return
    end
    emit({ type = 'callback_response', id = msg.id, name = msg.name, ok = true, handled = true, results = encoded, argc = out.n })
end

local function reload()
    pendingReload = false
    local ok, err = loadDriver()
    if ok then
        local handlers = handlerNames()
        emit({ type = 'reloaded', ok = true, handlers = { n = #handlers, unpack(handlers) } })
    else
        emit({ type = 'reloaded', ok = false, error = tostring(err) })
    end
end

local pendingLoad = false

local function load()
    pendingLoad = false
    local ok, err = loadDriver()
    if not ok then fatal(err) end
    loaded = true
    local handlers = handlerNames()
    emit({ type = 'ready', entry = opts.entry, handlers = { n = #handlers, unpack(handlers) } })
end

handleControlMessage = function(msg)
    if msg.type == 'load' then
        pendingLoad = true
    elseif msg.type == 'reload' then
        pendingReload = loaded
    elseif msg.type == 'shutdown' then
        shuttingDown = true
    end
end

while not shuttingDown do
    if pendingLoad then
        load()
    elseif pendingReload then
        reload()
    elseif loaded and #pendingCallbacks > 0 then
        dispatch(table.remove(pendingCallbacks, 1))
    else
        local msg = readMessage()
        if msg == nil then break end
        if msg.type == 'callback' then
            pendingCallbacks[#pendingCallbacks + 1] = msg
        else
            handleControlMessage(msg)
        end
    end
end

os.exit(0)

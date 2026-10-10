-- Exports: module table (require('json'))
--
-- The Remote4 wire codec. One encoder and one decoder, shared byte-for-byte
-- by the stub (on the controller) and the runtime (on the laptop), so a value
-- survives the round trip with the same shape on both ends. See
-- docs/remote4-protocol.md "Value encoding".
--
-- Why not C4:JsonEncode? It serialises a sequence as an object with
-- stringified integer keys on real firmware (DF-453), and the runtime has no
-- C4 at all.
--
-- Positional lists (call arguments, results) may contain nils. They are
-- encoded as arrays whose length is the table's `n` field when present, and
-- decoded to tables carrying `n`, so trailing nils are not lost.

local M = {}

M.null = setmetatable({}, { __tostring = function() return 'json.null' end })

local ESCAPES = {
    ['"'] = '\\"', ['\\'] = '\\\\', ['\n'] = '\\n', ['\r'] = '\\r',
    ['\t'] = '\\t', ['\b'] = '\\b', ['\f'] = '\\f',
}

local function encodeString(s)
    return '"' .. s:gsub('[%z\1-\31\\"]', function(c)
        return ESCAPES[c] or string.format('\\u%04x', string.byte(c))
    end) .. '"'
end

-- A table is an array when it carries an explicit `n` (a packed list, nils
-- allowed) or when its keys are exactly 1..#t. An empty table is an empty
-- object: on the wire the protocol's empty containers are tParams-style maps
-- far more often than lists, and lists are packed with `n` anyway.
local function arrayLength(t)
    if type(t.n) == 'number' then return t.n end
    local n = 0
    for k in pairs(t) do
        if type(k) ~= 'number' or k < 1 or k % 1 ~= 0 then return nil end
        n = n + 1
    end
    if n == 0 then return nil end
    for i = 1, n do
        if t[i] == nil then return nil end
    end
    return n
end

local function encodeNumber(v)
    if v ~= v or v == math.huge or v == -math.huge then return 'null' end
    if v % 1 == 0 and math.abs(v) < 1e15 then return string.format('%d', v) end
    return string.format('%.17g', v)
end

local encode

local function encodeTable(t, seen)
    if seen[t] then error('json.encode: cycle detected', 0) end
    seen[t] = true
    local n = arrayLength(t)
    local out
    if n then
        local parts = {}
        for i = 1, n do parts[i] = encode(t[i], seen) end
        out = '[' .. table.concat(parts, ',') .. ']'
    else
        -- Deterministic key order so identical values encode identically
        -- (specs compare strings; logs diff cleanly).
        local keys = {}
        for k in pairs(t) do
            if k ~= 'n' or type(t.n) ~= 'number' then keys[#keys + 1] = tostring(k) end
        end
        table.sort(keys)
        local parts = {}
        for i, k in ipairs(keys) do
            local v = t[k]
            if v == nil then v = t[tonumber(k)] end
            parts[i] = encodeString(k) .. ':' .. encode(v, seen)
        end
        out = '{' .. table.concat(parts, ',') .. '}'
    end
    seen[t] = nil
    return out
end

encode = function(value, seen)
    local t = type(value)
    if value == nil or value == M.null then
        return 'null'
    elseif t == 'boolean' then
        return value and 'true' or 'false'
    elseif t == 'number' then
        return encodeNumber(value)
    elseif t == 'string' then
        return encodeString(value)
    elseif t == 'table' then
        return encodeTable(value, seen or {})
    end
    error('json.encode: cannot encode ' .. t, 0)
end

function M.encode(value)
    return encode(value, {})
end

-- Decoder ------------------------------------------------------------------

local function decodeError(str, pos, msg)
    error(string.format('json.decode: %s at position %d', msg, pos), 0)
end

local function skipWhitespace(str, pos)
    local _, e = str:find('^[ \t\r\n]*', pos)
    return e + 1
end

local function utf8Char(code)
    if code < 0x80 then return string.char(code) end
    if code < 0x800 then
        return string.char(0xC0 + math.floor(code / 0x40), 0x80 + code % 0x40)
    end
    if code < 0x10000 then
        return string.char(0xE0 + math.floor(code / 0x1000),
            0x80 + math.floor(code / 0x40) % 0x40, 0x80 + code % 0x40)
    end
    return string.char(0xF0 + math.floor(code / 0x40000),
        0x80 + math.floor(code / 0x1000) % 0x40,
        0x80 + math.floor(code / 0x40) % 0x40, 0x80 + code % 0x40)
end

local UNESCAPES = { ['"'] = '"', ['\\'] = '\\', ['/'] = '/', b = '\b', f = '\f', n = '\n', r = '\r', t = '\t' }

local function decodeString(str, pos)
    local parts, i = {}, pos + 1
    while true do
        local c = str:sub(i, i)
        if c == '' then decodeError(str, pos, 'unterminated string') end
        if c == '"' then return table.concat(parts), i + 1 end
        if c == '\\' then
            local e = str:sub(i + 1, i + 1)
            if e == 'u' then
                local hex = str:sub(i + 2, i + 5)
                if not hex:match('^%x%x%x%x$') then decodeError(str, i, 'bad unicode escape') end
                local code = tonumber(hex, 16)
                i = i + 6
                -- Surrogate pair.
                if code >= 0xD800 and code <= 0xDBFF and str:sub(i, i + 1) == '\\u' then
                    local lo = tonumber(str:sub(i + 2, i + 5), 16)
                    if lo and lo >= 0xDC00 and lo <= 0xDFFF then
                        code = 0x10000 + (code - 0xD800) * 0x400 + (lo - 0xDC00)
                        i = i + 6
                    end
                end
                parts[#parts + 1] = utf8Char(code)
            elseif UNESCAPES[e] then
                parts[#parts + 1] = UNESCAPES[e]
                i = i + 2
            else
                decodeError(str, i, 'bad escape')
            end
        else
            local run = str:match('^[^"\\]+', i)
            parts[#parts + 1] = run
            i = i + #run
        end
    end
end

local decodeValue

local function decodeArray(str, pos)
    local out, n = {}, 0
    pos = skipWhitespace(str, pos + 1)
    if str:sub(pos, pos) == ']' then
        out.n = 0
        return out, pos + 1
    end
    while true do
        local v
        v, pos = decodeValue(str, pos)
        n = n + 1
        if v ~= M.null then out[n] = v end
        pos = skipWhitespace(str, pos)
        local c = str:sub(pos, pos)
        if c == ']' then
            out.n = n
            return out, pos + 1
        end
        if c ~= ',' then decodeError(str, pos, "expected ',' or ']'") end
        pos = skipWhitespace(str, pos + 1)
    end
end

local function decodeObject(str, pos)
    local out = {}
    pos = skipWhitespace(str, pos + 1)
    if str:sub(pos, pos) == '}' then return out, pos + 1 end
    while true do
        if str:sub(pos, pos) ~= '"' then decodeError(str, pos, 'expected string key') end
        local k, v
        k, pos = decodeString(str, pos)
        pos = skipWhitespace(str, pos)
        if str:sub(pos, pos) ~= ':' then decodeError(str, pos, "expected ':'") end
        v, pos = decodeValue(str, skipWhitespace(str, pos + 1))
        if v ~= M.null then out[k] = v end
        pos = skipWhitespace(str, pos)
        local c = str:sub(pos, pos)
        if c == '}' then return out, pos + 1 end
        if c ~= ',' then decodeError(str, pos, "expected ',' or '}'") end
        pos = skipWhitespace(str, pos + 1)
    end
end

decodeValue = function(str, pos)
    pos = skipWhitespace(str, pos)
    local c = str:sub(pos, pos)
    if c == '{' then return decodeObject(str, pos) end
    if c == '[' then return decodeArray(str, pos) end
    if c == '"' then return decodeString(str, pos) end
    if str:sub(pos, pos + 3) == 'true' then return true, pos + 4 end
    if str:sub(pos, pos + 4) == 'false' then return false, pos + 5 end
    if str:sub(pos, pos + 3) == 'null' then return M.null, pos + 4 end
    local num = str:match('^-?%d+%.?%d*[eE]?[-+]?%d*', pos)
    if num and num ~= '' and num ~= '-' then
        local v = tonumber(num)
        if v then return v, pos + #num end
    end
    decodeError(str, pos, 'unexpected character ' .. (c == '' and 'EOF' or ("'" .. c .. "'")))
end

-- Decodes a JSON document. A top-level `null` decodes to json.null (so a
-- caller can tell it from an empty document); inside containers nulls become
-- absent keys / nil slots (arrays carry `n` so the slot count survives).
function M.decode(str)
    if type(str) ~= 'string' then error('json.decode: expected string, got ' .. type(str), 0) end
    local v, pos = decodeValue(str, 1)
    pos = skipWhitespace(str, pos)
    if pos <= #str then decodeError(str, pos, 'trailing garbage') end
    return v
end

-- Packs varargs into a list carrying `n`, the shape the encoder turns into a
-- JSON array with trailing nulls preserved.
function M.pack(...)
    return { n = select('#', ...), ... }
end

return M

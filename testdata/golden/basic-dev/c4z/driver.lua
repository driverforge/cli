package.preload['config'] = (function (...)
return { env = "dev" }
 end)
package.preload['lib.util'] = (function (...)
local M = {}

function M.hi()
	return "hi"
end

return M
 end)
local util = require("lib.util")
local config = require("config")

print(util.hi(), config.env)

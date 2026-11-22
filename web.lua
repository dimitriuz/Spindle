-- cliamp-plugin-web: LAN web remote. Spawns and supervises cliamp-web.
local p = plugin.register({
    name        = "web",
    type        = "hook",
    version     = "1.1.0",
    description = "LAN web interface (spawns the cliamp-web bridge)",
    permissions = {"exec"},
})

local port  = tostring(p:config("port") or "8730")
local lan   = p:config("lan")
local hosts = p:config("allowed_hosts")
local bin   = p:config("binary") or "cliamp-web"

-- same lookup as the bridge's own default, so status.json is found
local datadir = (os.getenv("XDG_DATA_HOME") or (os.getenv("HOME") .. "/.local/share")) .. "/cliamp/web"
local restarts, handle, quitting = 0, nil, false

local function arglist()
    local args = {"serve", "--port", port}
    if lan then args[#args + 1] = "--lan" end
    if hosts then
        args[#args + 1] = "--allowed-hosts"
        args[#args + 1] = tostring(hosts)
    end
    return args
end

local function read_status()
    local raw = cliamp.fs.read(datadir .. "/status.json")
    if not raw then return nil end
    return cliamp.json.decode(raw)
end

local function start()
    if quitting then return end
    local h, err = cliamp.exec.run(bin, arglist(), {
        on_stdout = function(line) cliamp.log.info("bridge: " .. line) end,
        on_stderr = function(line) cliamp.log.info("bridge: " .. line) end,
        on_exit = function(code)
            handle = nil
            if quitting or code == -1 then return end
            if restarts < 3 then
                restarts = restarts + 1
                cliamp.log.warn(string.format(
                    "bridge exited (%s); restart %d/3", tostring(code), restarts))
                cliamp.timer.after(2, start)
            else
                cliamp.log.error("bridge keeps failing; giving up this session")
                cliamp.message("web: bridge died, giving up")
            end
        end,
    })
    if not h then
        cliamp.log.error("cannot start '" .. bin .. "'; install it and add it to [plugins] allowed_binaries")
    else
        handle = h
        -- a bridge that stayed up a minute was healthy: forgive earlier crashes,
        -- so one bad hour does not use up the retries for the whole session
        cliamp.timer.after(60, function()
            if handle == h and h:alive() then restarts = 0 end
        end)
    end
end

p:on("app.start", function()
    start()
    cliamp.timer.after(1.5, function()
        local st = read_status()
        if st and st.url then
            cliamp.message("web remote: " .. st.url, 10)
        end
    end)
end)

p:on("app.quit", function()
    quitting = true
    -- do not leave the bridge holding the port for the next cliamp
    if handle then handle:cancel() end
end)

p:command("url", function()
    local st = read_status()
    if not st then return "bridge not up (check plugins.log)" end
    return st.url
end)

p:command("status", function()
    local st = read_status()
    if not st then return "offline" end
    return string.format("up :%s pid=%s restarts=%d", st.port, tostring(st.pid), restarts)
end)

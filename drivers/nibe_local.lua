-- NIBE S-series Heat Pump — LOCAL REST API driver (READ-ONLY telemetry)
-- Emits: metrics only (compressor power, energy meters, temperatures, …)
--        into the long-format TS DB via host.emit_metric. NO control.
-- Protocol: HTTPS (NIBE "Local REST API", self-described at https://<ip>:8443/)
--
-- This is the local-network twin of drivers/myuplink.lua. Instead of the
-- MyUplink cloud (OAuth + internet round-trip), it reads the pump directly
-- over the LAN. The local API is RICHER than the cloud one: every point
-- ships its own metadata (modbus register, unit, exact divisor, writable
-- flag), so scaling is exact — no °C×10 heuristic. ~980 points come back in
-- one bulk GET, so the whole register map lands in the TS DB for life.
--
-- Observe-only by design: the pump is left in read-only mode (aidMode=off),
-- so this driver cannot actuate anything and cannot cause harm.
--
-- Site sign convention: a heat pump is a LOAD. Its electrical draw would be
-- positive W flowing into the site at the grid boundary — but this driver
-- emits diagnostics via host.emit_metric only (never host.emit("meter"|…)),
-- so it performs NO sign conversion and never double-counts against the real
-- grid meter. The thermal/load models consume hp_power_w etc. as twins.
--
-- AUTH + TRANSPORT:
--   The local API uses HTTP Basic auth over HTTPS with a SELF-SIGNED
--   certificate. The system trust store can't validate it, so the driver
--   relies on certificate PINNING in the host: grant
--   capabilities.http.tls_pin_sha256 with the pump's cert fingerprint
--   (the "fingeravtryck" shown in the myUplink app, or from
--   `openssl s_client -connect <ip>:8443 | openssl x509 -fingerprint -sha256`).
--   That pins exactly one leaf cert — a swapped cert (MITM on the LAN, which
--   would otherwise capture the Basic-auth password) is rejected at the
--   handshake. Do NOT fall back to blanket insecure-skip-verify.
--
-- Config example (config.yaml):
--   drivers:
--     - name: nibe
--       lua: drivers/nibe_local.lua
--       config:
--         host: "192.168.1.180"
--         port: 8443
--         username: "<local-api-username>"
--         password: "<local-api-password>"   # masked via config_secrets
--         # device_id: "..."        # optional; auto-detected if omitted
--       capabilities:
--         http:
--           allowed_hosts: ["192.168.1.180:8443"]
--           tls_pin_sha256: "<64-hex-char certificate fingerprint>"
--
-- The four heating-UI headline metrics map to NIBE S735 variable ids by
-- default; override per model via param_power_id / param_hw_temp_id /
-- param_indoor_temp_id / param_outdoor_temp_id if yours differs (find them
-- in the bulk GET /api/v1/devices/<serial>/points).

DRIVER = {
  id           = "nibe-local",
  name         = "NIBE S-series (local REST API)",
  manufacturer = "NIBE",
  version      = "1.0.0",
  protocols    = { "http" },
  capabilities = { "apicreds" },
  description  = "Read-only NIBE S-series heat-pump telemetry over the on-prem Local REST API (HTTPS + Basic auth, self-signed cert pinned via tls_pin_sha256). Emits compressor/used power, lifetime energy meters, and the full ~980-point register map. Observe-only — no control.",
  homepage     = "https://www.nibe.eu",
  authors      = { "HuggeK", "forty-two-watts contributors" },
  tested_models = { "NIBE S735" },
  verification_status = "beta",
  config_secrets = { "password" },
  connection_defaults = { port = 8443 },
}

PROTOCOL = "http"

-- ---- Runtime state -------------------------------------------------------

local base_url      = nil    -- https://<host>:<port>
local auth_value    = nil    -- "Basic <base64(user:pass)>"
local serial        = nil    -- device id (NIBE serial number) used in the path

-- Self-heal: the pump can be briefly unreachable at boot / after a network
-- blip. Rather than wedge on a nil serial (which needed a manual restart),
-- driver_poll retries device detection on this backoff.
local SETUP_RETRY_MS = 30000
local last_setup_ms  = nil
local POLL_INTERVAL_MS = 60000

-- Canonical headline ids (NIBE S735 local-API variableIds). Built into a
-- lookup in driver_init so config overrides can move them per model. The
-- first four names are the ones web/heating.js + the thermal twin read.
local DEFAULT_POWER_ID   = "1801"   -- Compressor power input (W)
local DEFAULT_USED_ID    = "22130"  -- Instantaneous used power (W)
local DEFAULT_HW_ID      = "11"     -- Hot water top (BT7) (°C)
local DEFAULT_INDOOR_ID  = "158"    -- Room average temp clim. system 1 (BT50)
local DEFAULT_OUTDOOR_ID = "4"      -- Current outdoor temperature (BT1)
local DEFAULT_ECONS_ID   = "28393"  -- Tot. consumption (kWh)
local DEFAULT_EPROD_ID   = "28392"  -- Tot. production (kWh)
local DEFAULT_DM_ID      = "781"    -- Degree minutes
local CANON = {}   -- id(string) -> { name = "...", watts = bool }

-- ---- Helpers -------------------------------------------------------------

-- Pure-Lua base64 (no host builtin). Used once per init to build the
-- Basic-auth header value.
local b64chars = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/'
local function base64_encode(data)
    return ((data:gsub('.', function(x)
        local r, b = '', x:byte()
        for i = 8, 1, -1 do r = r .. (b % 2 ^ i - b % 2 ^ (i - 1) > 0 and '1' or '0') end
        return r
    end) .. '0000'):gsub('%d%d%d?%d?%d?%d?', function(x)
        if #x < 6 then return '' end
        local c = 0
        for i = 1, 6 do c = c + (x:sub(i, i) == '1' and 2 ^ (6 - i) or 0) end
        return b64chars:sub(c + 1, c + 1)
    end) .. ({ '', '==', '=' })[#data % 3 + 1])
end

-- The "not connected" sentinel a NIBE variable reports per size. An
-- unconnected sensor returns this (e.g. an absent BT50 room sensor is
-- -32768 for s16) — and the API marks it isOk=true anyway, so we filter
-- by size, not by isOk.
local function size_sentinel(size)
    if size == "s8"  then return -128 end
    if size == "s16" then return -32768 end
    if size == "s32" then return -2147483648 end
    if size == "u8"  then return 255 end
    if size == "u16" then return 65535 end
    if size == "u32" then return 4294967295 end
    return nil
end

-- Turn a point title into a stable hp_ snake_case metric name. NIBE titles
-- embed soft hyphens (U+00AD = bytes 0xC2 0xAD) inside long words
-- ("Compres­sor", "Instant­aneous"); strip them first so the name reads
-- "compressor", not "compres_sor". Remaining non-ASCII / punctuation
-- collapses to single underscores. Empty falls back to the id.
local function sanitize_metric_name(title, id)
    local s = title or ""
    s = string.gsub(s, "\194\173", "")        -- soft hyphen
    s = string.lower(s)
    s = string.gsub(s, "[^a-z0-9]+", "_")
    s = string.gsub(s, "^_+", "")
    s = string.gsub(s, "_+$", "")
    if s == "" then s = "p" .. tostring(id) end
    return "hp_" .. s
end

-- Watts normalisation for the power headline metrics: some models report
-- compressor power in kW, others in W. Emit W either way.
local function to_watts(value, unit)
    if unit == "kW" then return value * 1000.0, "W" end
    return value, (unit ~= "" and unit or "W")
end

local function auth_headers()
    return { Authorization = auth_value, Accept = "application/json" }
end

local function api_get(path)
    local resp, err = host.http_get(base_url .. path, auth_headers())
    if err then return nil, tostring(err) end
    local data = host.json_decode(resp)
    if not data then return nil, "json decode failed" end
    return data, nil
end

-- ---- Setup ---------------------------------------------------------------

local function detect_serial()
    local data, err = api_get("/api/v1/devices")
    if err then
        host.log("warn", "NIBE: /api/v1/devices failed: " .. err)
        return nil
    end
    local devs = data.devices
    if type(devs) == "table" and devs[1] and devs[1].product then
        local p = devs[1].product
        if p.serialNumber and p.serialNumber ~= "" then
            host.log("info", "NIBE: detected " .. tostring(p.manufacturer) ..
                " " .. tostring(p.serialNumber) .. " (fw " .. tostring(p.firmwareId) .. ")")
            return p.serialNumber
        end
    end
    host.log("error", "NIBE: no device serial in /api/v1/devices response")
    return nil
end

-- Bring the driver to "ready" (serial known). Safe to call repeatedly;
-- rate-limited by SETUP_RETRY_MS. Returns true once serial is established.
local function try_setup()
    if serial then return true end
    local now = host.millis()
    if last_setup_ms ~= nil and (now - last_setup_ms) < SETUP_RETRY_MS then
        return false
    end
    last_setup_ms = now
    serial = detect_serial()
    if not serial then return false end
    host.set_sn(serial)
    host.log("info", "NIBE: ready (read-only) serial=" .. serial)
    return true
end

-- ---- Lifecycle -----------------------------------------------------------

function driver_init(config)
    host.set_make("NIBE")
    config = config or {}

    local function s(v) return (v ~= nil and v ~= "") and tostring(v) or nil end
    local username = s(config.username) or ""
    local password = s(config.password) or ""
    serial         = s(config.device_id)

    -- base_url override exists for tests; production builds it from host:port.
    base_url = s(config.base_url)
    if not base_url then
        local host_ip = s(config.host)
        local port    = s(config.port) or "8443"
        if host_ip then base_url = "https://" .. host_ip .. ":" .. port end
    end
    auth_value = "Basic " .. base64_encode(username .. ":" .. password)

    if config.poll_interval_ms ~= nil then
        POLL_INTERVAL_MS = tonumber(config.poll_interval_ms) or POLL_INTERVAL_MS
    end
    if config.setup_retry_ms ~= nil then
        SETUP_RETRY_MS = tonumber(config.setup_retry_ms) or SETUP_RETRY_MS
    end

    -- Build the canonical headline lookup (ids overridable per model).
    local function ov(k, d) return s(config[k]) or d end
    CANON = {}
    CANON[ov("param_power_id",   DEFAULT_POWER_ID)]   = { name = "hp_power_w",            watts = true }
    CANON[ov("param_used_id",    DEFAULT_USED_ID)]    = { name = "hp_used_power_w",       watts = true }
    CANON[ov("param_hw_temp_id", DEFAULT_HW_ID)]      = { name = "hp_hw_top_temp_c" }
    CANON[ov("param_indoor_temp_id", DEFAULT_INDOOR_ID)] = { name = "hp_indoor_temp_c" }
    CANON[ov("param_outdoor_temp_id", DEFAULT_OUTDOOR_ID)] = { name = "hp_outdoor_temp_c" }
    CANON[ov("param_energy_consumed_id", DEFAULT_ECONS_ID)] = { name = "hp_energy_consumed_kwh" }
    CANON[ov("param_energy_produced_id", DEFAULT_EPROD_ID)] = { name = "hp_energy_produced_kwh" }
    CANON[ov("param_degree_minutes_id", DEFAULT_DM_ID)]   = { name = "hp_degree_minutes" }

    if not base_url then
        host.log("error", "NIBE: 'host' (pump IP) is required")
        return
    end
    if username == "" or password == "" then
        host.log("error", "NIBE: username and password are required")
        return
    end

    host.set_poll_interval(POLL_INTERVAL_MS)
    -- Best-effort initial detection; driver_poll self-heals if it fails.
    if not try_setup() then
        host.log("warn", "NIBE: initial setup did not complete — will retry automatically")
    end
end

function driver_poll()
    if not base_url then return SETUP_RETRY_MS end
    if not serial then
        if not try_setup() then return SETUP_RETRY_MS end
    end

    local data, err = api_get("/api/v1/devices/" .. serial .. "/points")
    if err then
        host.log("warn", "NIBE: points poll failed: " .. err)
        return POLL_INTERVAL_MS
    end

    local seen = {}
    for id, pt in pairs(data) do
        local m = pt.metadata
        local v = pt.value
        if type(m) == "table" and type(v) == "table" and type(v.integerValue) == "number" then
            local raw = v.integerValue
            local sentinel = size_sentinel(m.variableSize)
            if not (sentinel and raw == sentinel) then
                local div = tonumber(m.divisor) or 1
                if div == 0 then div = 1 end
                local scaled = raw / div
                local unit = m.unit or ""
                local canon = CANON[tostring(id)]
                if canon then
                    if canon.watts then
                        local w, u = to_watts(scaled, unit)
                        host.emit_metric(canon.name, w, u)
                    else
                        host.emit_metric(canon.name, scaled, unit)
                    end
                else
                    local name = sanitize_metric_name(pt.title, id)
                    if seen[name] then name = name .. "_" .. tostring(id) end
                    seen[name] = true
                    host.emit_metric(name, scaled, unit)
                end
            end
        end
    end

    return POLL_INTERVAL_MS
end

function driver_command(_action, _power_w, _cmd)
    -- Read-only: no actuation. The pump stays in aidMode=off.
    return false
end

function driver_default_mode()
    -- Read-only: nothing to release.
end

function driver_cleanup()
    serial       = nil
    last_setup_ms = nil
end

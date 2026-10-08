-- BeamWorlds Mod Studio: host-authored game test harness.
-- Placed at lua/ge/extensions/modstudio/test/harness.lua in an isolated user
-- folder. Loaded via -onLevelLoad_ext modstudio_test_harness (BeamNG resolves
-- single underscores to path separators: modstudio/test/harness).
-- Writes structured JSON observations and calls shutdown(0).

local M = {}
M.dependencies = {}

local observationPath
local durationSeconds
local targetVehicle
local wallElapsed
local vehicleSpawned
local simElapsed
local levelLoaded
local observations
local finished

local function writeObservations()
  if finished then return end
  finished = true
  if not observationPath or observationPath == "" then
    log("E", "modstudio_test", "observation path is not set")
    return
  end
  if #observations.errors == 0 then observations.errors = nil end
  if jsonWriteFile(observationPath, observations, true) then
    log("I", "modstudio_test", "wrote observations to " .. observationPath)
  else
    log("E", "modstudio_test", "could not open: " .. observationPath)
  end
  shutdown(0)
end

local function recordError(msg)
  log("E", "modstudio_test", msg)
  table.insert(observations.errors, msg)
end

local function identifyVehicle(vid)
  local name = ""
  local vehObj = scenetree.findObjectById(vid)
  if vehObj and type(vehObj.getJBeamFilename) == "function" then
    name = vehObj:getJBeamFilename() or ""
  end
  if name == "" then
    local mgr = extensions.core_vehicle_manager
    if mgr then
      local vd = mgr.getVehicleData(vid)
      if vd and vd.config then
        name = vd.config.model or ""
      end
      if name == "" and vd and vd.mainPartName then
        name = vd.mainPartName
      end
    end
  end
  return name
end

local function tryRecordVehicle(vid)
  if vehicleSpawned then return end
  local name = identifyVehicle(vid)
  if targetVehicle ~= "" and name ~= "" and name ~= targetVehicle then
    log("I", "modstudio_test", "ignoring vehicle: " .. name)
    return
  end
  if targetVehicle ~= "" and name == "" then
    log("I", "modstudio_test", "ignoring unidentified vehicle vid=" .. tostring(vid))
    return
  end
  vehicleSpawned = true
  observations.vehicleSpawned = true
  observations.vehicleName = name
  table.insert(observations.stages, {stage = "vehicleSpawned", at = wallElapsed, vehicle = name})
  log("I", "modstudio_test", "target vehicle: " .. name .. " vid=" .. tostring(vid))
end

local function checkExistingVehicles()
  if vehicleSpawned then return end
  for _, veh in ipairs(getAllVehicles and getAllVehicles() or {}) do
    if veh then
      tryRecordVehicle(veh:getId())
      if vehicleSpawned then return end
    end
  end
end

local function onExtensionLoaded()
  observations = {
    harnessVersion = 1,
    levelLoaded    = false,
    vehicleSpawned = false,
    vehicleName    = "",
    targetVehicle  = "",
    simSeconds     = 0,
    errors         = {},
    stages         = {},
  }
  finished       = false
  vehicleSpawned = false
  simElapsed     = 0
  wallElapsed    = 0
  levelLoaded    = false

  local config = jsonReadFile("/settings/modstudioGameTest.json")
  if type(config) ~= "table" or type(config.observationPath) ~= "string"
      or type(config.durationSeconds) ~= "number" or config.durationSeconds <= 0 then
    log("E", "modstudio_test", "missing or invalid host game-test configuration")
    return false
  end
  observationPath = config.observationPath
  durationSeconds = config.durationSeconds
  targetVehicle = config.vehicle or ""

  observations.targetVehicle = targetVehicle
  table.insert(observations.stages, {stage = "init", at = 0})
  log("I", "modstudio_test", "harness init: duration=" .. tostring(durationSeconds) .. "s vehicle=" .. tostring(targetVehicle))
end

local function onClientPostStartMission(levelPath)
  levelLoaded = true
  observations.levelLoaded = true
  observations.levelPath = levelPath or ""
  table.insert(observations.stages, {stage = "levelLoaded", at = wallElapsed})
  log("I", "modstudio_test", "level loaded: " .. tostring(levelPath))
  checkExistingVehicles()
end

local function onVehicleSpawned(vid, vehicle)
  tryRecordVehicle(vid)
end

local function onUpdate(dtReal, dtSim, dtRaw)
  if finished then return end
  wallElapsed = wallElapsed + dtReal

  if wallElapsed > 120 and not levelLoaded then
    recordError("level did not load within 120s")
    observations.simSeconds = 0
    writeObservations()
    return
  end
  if wallElapsed > 180 then
    recordError("safety timeout 180s")
    observations.simSeconds = simElapsed
    writeObservations()
    return
  end
  if not vehicleSpawned then return end

  simElapsed = simElapsed + dtSim
  if simElapsed >= durationSeconds then
    observations.simSeconds = simElapsed
    table.insert(observations.stages, {stage = "simComplete", at = wallElapsed, simSeconds = simElapsed})
    log("I", "modstudio_test", string.format("sim complete: %.2fs sim, %.2fs wall", simElapsed, wallElapsed))
    writeObservations()
  end
end

M.onExtensionLoaded        = onExtensionLoaded
M.onClientPostStartMission = onClientPostStartMission
M.onVehicleSpawned         = onVehicleSpawned
M.onUpdate                 = onUpdate

return M

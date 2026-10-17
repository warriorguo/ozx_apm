using System;
using System.Security.Cryptography;
using System.Text;
using UnityEngine;
using OzxApm.Models;

namespace OzxApm.Utils
{
    public static class DeviceInfo
    {
        private static string _cachedDeviceId;
        private static Platform _cachedPlatform;
        private static string _cachedNetworkType;

        private const string DeviceIdKey = "ozx_apm_device_id";
        private static bool _deviceIdPersisted;

        /// <summary>
        /// Gets an anonymous, resettable device identifier (and stores it).
        /// </summary>
        public static string GetDeviceId()
        {
            return GetDeviceId(true);
        }

        /// <summary>
        /// Gets the device identifier. With persist=false a newly generated id is
        /// only kept in memory (no PlayerPrefs write, e.g. before consent); call
        /// PersistDeviceId once writing is allowed.
        /// </summary>
        public static string GetDeviceId(bool persist)
        {
            if (string.IsNullOrEmpty(_cachedDeviceId))
            {
                // Reading PlayerPrefs writes nothing.
                _cachedDeviceId = PlayerPrefs.GetString(DeviceIdKey, "");
                _deviceIdPersisted = !string.IsNullOrEmpty(_cachedDeviceId);

                if (!_deviceIdPersisted)
                {
                    // Generate a new anonymous device ID
                    _cachedDeviceId = GenerateAnonymousId();
                }
            }

            if (persist)
                PersistDeviceId();

            return _cachedDeviceId;
        }

        /// <summary>
        /// Writes the current device id to PlayerPrefs if it is not stored yet.
        /// </summary>
        public static void PersistDeviceId()
        {
            if (_deviceIdPersisted || string.IsNullOrEmpty(_cachedDeviceId))
                return;
            PlayerPrefs.SetString(DeviceIdKey, _cachedDeviceId);
            PlayerPrefs.Save();
            _deviceIdPersisted = true;
        }

        /// <summary>
        /// Resets the device ID (for privacy compliance)
        /// </summary>
        public static void ResetDeviceId()
        {
            ResetDeviceId(true);
        }

        /// <summary>
        /// Resets the device ID; with persist=false the new id is not written yet.
        /// </summary>
        public static void ResetDeviceId(bool persist)
        {
            _cachedDeviceId = GenerateAnonymousId();
            _deviceIdPersisted = false;
            if (persist)
                PersistDeviceId();
        }

        /// <summary>Test seam: forget the cached id.</summary>
        internal static void ClearCacheForTests()
        {
            _cachedDeviceId = null;
            _deviceIdPersisted = false;
        }

        /// <summary>
        /// Gets the current platform
        /// </summary>
        public static Platform GetPlatform()
        {
            if (_cachedPlatform != Platform.Unknown)
                return _cachedPlatform;

            // C# 8 syntax (no "or" patterns): the package supports Unity 2020.3.
            switch (Application.platform)
            {
                case RuntimePlatform.Android: _cachedPlatform = Platform.Android; break;
                case RuntimePlatform.IPhonePlayer: _cachedPlatform = Platform.iOS; break;
                case RuntimePlatform.WindowsPlayer:
                case RuntimePlatform.WindowsEditor: _cachedPlatform = Platform.Windows; break;
                case RuntimePlatform.OSXPlayer:
                case RuntimePlatform.OSXEditor: _cachedPlatform = Platform.MacOS; break;
                case RuntimePlatform.LinuxPlayer:
                case RuntimePlatform.LinuxEditor: _cachedPlatform = Platform.Linux; break;
                case RuntimePlatform.WebGLPlayer: _cachedPlatform = Platform.WebGL; break;
                default: _cachedPlatform = Platform.Unknown; break;
            }

            return _cachedPlatform;
        }

        /// <summary>
        /// Gets platform as string for API
        /// </summary>
        public static string GetPlatformString()
        {
            return GetPlatform().ToString();
        }

        /// <summary>
        /// Gets the OS version
        /// </summary>
        public static string GetOSVersion()
        {
            return SystemInfo.operatingSystem;
        }

        /// <summary>
        /// Gets the device model
        /// </summary>
        public static string GetDeviceModel()
        {
            return SystemInfo.deviceModel;
        }

        /// <summary>
        /// Gets CPU information
        /// </summary>
        public static string GetCPU()
        {
            return SystemInfo.processorType;
        }

        /// <summary>
        /// Gets GPU information
        /// </summary>
        public static string GetGPU()
        {
            return SystemInfo.graphicsDeviceName;
        }

        /// <summary>
        /// Gets the RAM class: the nominal memory size of the device ("2GB", "3GB",
        /// "4GB", "6GB", "8GB", "12GB", "16GB", "24GB+"; "1GB" and below), sent with
        /// every event (APM-5). Devices report less than their nominal size (an 8GB
        /// phone reports ~7.5GB), so the reported size is rounded up to the class.
        /// </summary>
        public static string GetRAMClass()
        {
            return RamClassFromMB(SystemInfo.systemMemorySize);
        }

        internal static string RamClassFromMB(int memoryMB)
        {
            if (memoryMB <= 0)
                return "unknown";
            if (memoryMB <= 1536)
                return "1GB";
            if (memoryMB <= 2560)
                return "2GB";
            if (memoryMB <= 3584)
                return "3GB";
            if (memoryMB <= 5120)
                return "4GB";
            if (memoryMB <= 7168)
                return "6GB";
            if (memoryMB <= 10240)
                return "8GB";
            if (memoryMB <= 14336)
                return "12GB";
            if (memoryMB <= 20480)
                return "16GB";
            return "24GB+";
        }

        /// <summary>
        /// Gets current network type ("WiFi", "Cellular", "None"). Main thread.
        /// </summary>
        public static string GetNetworkType()
        {
            return NetworkTypeName(Application.internetReachability);
        }

        /// <summary>Network type name for a reachability value; constant strings, no allocation.</summary>
        internal static string NetworkTypeName(NetworkReachability reachability)
        {
            switch (reachability)
            {
                case NetworkReachability.ReachableViaLocalAreaNetwork: return "WiFi";
                case NetworkReachability.ReachableViaCarrierDataNetwork: return "Cellular";
                case NetworkReachability.NotReachable: return "None";
                default: return "Unknown";
            }
        }

        /// <summary>"release", "dev" or "editor" for the configured build type (Auto: detected).</summary>
        internal static string BuildTypeName(ApmBuildType configured, bool isEditor, bool isDebugBuild)
        {
            switch (configured)
            {
                case ApmBuildType.Release: return "release";
                case ApmBuildType.Dev: return "dev";
                case ApmBuildType.Editor: return "editor";
            }
            // Debug.isDebugBuild is always true in the Editor: check the Editor first.
            if (isEditor)
                return "editor";
            return isDebugBuild ? "dev" : "release";
        }

        /// <summary>
        /// Gets total system memory in MB
        /// </summary>
        public static int GetSystemMemoryMB()
        {
            return SystemInfo.systemMemorySize;
        }

        /// <summary>
        /// Gets used memory in MB (approximate)
        /// </summary>
        public static float GetUsedMemoryMB()
        {
            return (float)GC.GetTotalMemory(false) / (1024 * 1024);
        }

        private static string GenerateAnonymousId()
        {
            // Create a hash from device info that doesn't contain PII
            string seed = $"{SystemInfo.deviceUniqueIdentifier}_{DateTime.UtcNow.Ticks}_{Guid.NewGuid()}";

            using (var sha256 = SHA256.Create())
            {
                byte[] hashBytes = sha256.ComputeHash(Encoding.UTF8.GetBytes(seed));
                // Take first 16 bytes for a shorter ID
                return BitConverter.ToString(hashBytes, 0, 16).Replace("-", "").ToLowerInvariant();
            }
        }
    }
}

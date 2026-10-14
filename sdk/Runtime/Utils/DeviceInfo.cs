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

            _cachedPlatform = Application.platform switch
            {
                RuntimePlatform.Android => Platform.Android,
                RuntimePlatform.IPhonePlayer => Platform.iOS,
                RuntimePlatform.WindowsPlayer or RuntimePlatform.WindowsEditor => Platform.Windows,
                RuntimePlatform.OSXPlayer or RuntimePlatform.OSXEditor => Platform.MacOS,
                RuntimePlatform.LinuxPlayer or RuntimePlatform.LinuxEditor => Platform.Linux,
                RuntimePlatform.WebGLPlayer => Platform.WebGL,
                _ => Platform.Unknown
            };

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
        /// Gets RAM class (Low/Medium/High based on system memory)
        /// </summary>
        public static string GetRAMClass()
        {
            int memoryMB = SystemInfo.systemMemorySize;
            if (memoryMB < 2048)
                return "Low";
            if (memoryMB < 4096)
                return "Medium";
            return "High";
        }

        /// <summary>
        /// Gets current network type
        /// </summary>
        public static string GetNetworkType()
        {
            return Application.internetReachability switch
            {
                NetworkReachability.ReachableViaLocalAreaNetwork => "WiFi",
                NetworkReachability.ReachableViaCarrierDataNetwork => "Cellular",
                NetworkReachability.NotReachable => "None",
                _ => "Unknown"
            };
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

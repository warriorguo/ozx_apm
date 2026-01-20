using System.Collections.Generic;
using UnityEngine;
using UnityEngine.SceneManagement;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Collectors
{
    /// <summary>
    /// Tracks scene load timing
    /// </summary>
    public class SceneLoadTracker : ICollector
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;

        private bool _isActive;

        private class SceneLoadInfo
        {
            public string SceneName;
            public float LoadStartTime;
            public float LoadEndTime;
            public float ActivateStartTime;
            public float ActivateEndTime;
            public bool IsComplete;
        }

        private readonly Dictionary<string, SceneLoadInfo> _pendingLoads = new Dictionary<string, SceneLoadInfo>();

        public bool IsActive => _isActive;

        public SceneLoadTracker(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        public void Start()
        {
            _isActive = true;

            // Subscribe to scene events
            SceneManager.sceneLoaded += OnSceneLoaded;
            SceneManager.sceneUnloaded += OnSceneUnloaded;
        }

        public void Update()
        {
            // Nothing to do in Update
        }

        public void Stop()
        {
            _isActive = false;

            SceneManager.sceneLoaded -= OnSceneLoaded;
            SceneManager.sceneUnloaded -= OnSceneUnloaded;

            _pendingLoads.Clear();
        }

        /// <summary>
        /// Call before starting an async scene load
        /// </summary>
        public void BeginSceneLoad(string sceneName)
        {
            if (!_isActive)
                return;

            var info = new SceneLoadInfo
            {
                SceneName = sceneName,
                LoadStartTime = Time.realtimeSinceStartup,
                IsComplete = false
            };

            _pendingLoads[sceneName] = info;
        }

        /// <summary>
        /// Call when async scene load is complete but not yet activated
        /// </summary>
        public void OnSceneLoadComplete(string sceneName)
        {
            if (!_isActive)
                return;

            if (_pendingLoads.TryGetValue(sceneName, out var info))
            {
                info.LoadEndTime = Time.realtimeSinceStartup;
                info.ActivateStartTime = Time.realtimeSinceStartup;
            }
        }

        private void OnSceneLoaded(Scene scene, LoadSceneMode mode)
        {
            if (!_isActive)
                return;

            string sceneName = scene.name;

            if (_pendingLoads.TryGetValue(sceneName, out var info))
            {
                info.ActivateEndTime = Time.realtimeSinceStartup;
                info.IsComplete = true;

                // Calculate timings
                float loadMs = (info.LoadEndTime - info.LoadStartTime) * 1000f;
                float activateMs = (info.ActivateEndTime - info.ActivateStartTime) * 1000f;

                // If we didn't get intermediate timing, assume all time is load time
                if (loadMs <= 0)
                {
                    loadMs = (info.ActivateEndTime - info.LoadStartTime) * 1000f;
                    activateMs = 0;
                }

                ReportSceneLoad(sceneName, loadMs, activateMs);
                _pendingLoads.Remove(sceneName);
            }
            else
            {
                // Synchronous load - we don't have start time
                // Just record a minimal event
                ReportSceneLoad(sceneName, 0, 0);
            }
        }

        private void OnSceneUnloaded(Scene scene)
        {
            // Could track unload times if needed
            _pendingLoads.Remove(scene.name);
        }

        private void ReportSceneLoad(string sceneName, float loadMs, float activateMs)
        {
            var evt = new SceneLoadEvent
            {
                scene_name = sceneName,
                load_ms = loadMs,
                activate_ms = activateMs
            };

            _client.EnqueueEvent(evt);

            if (loadMs > 0 || activateMs > 0)
            {
                ApmClient.Log(LogLevel.Debug, $"Scene load: {sceneName} (load={loadMs:F0}ms, activate={activateMs:F0}ms)");
            }
        }

        /// <summary>
        /// Helper to wrap async scene loading with automatic tracking
        /// </summary>
        public AsyncOperation LoadSceneTracked(string sceneName, LoadSceneMode mode = LoadSceneMode.Single)
        {
            BeginSceneLoad(sceneName);
            var op = SceneManager.LoadSceneAsync(sceneName, mode);
            return op;
        }
    }
}

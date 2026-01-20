using System;
using System.Collections;
using System.Collections.Generic;
using System.IO;
using System.IO.Compression;
using System.Text;
using UnityEngine;
using UnityEngine.Networking;
using OzxApm.Core;
using OzxApm.Models;
using OzxApm.Utils;

namespace OzxApm.Network
{
    /// <summary>
    /// Handles HTTP reporting of events to the server
    /// </summary>
    public class EventReporter
    {
        private readonly ApmConfig _config;
        private readonly OfflineStorage _offlineStorage;
        private readonly string _ingestUrl;

        private bool _isSending;
        private int _consecutiveFailures;
        private const int MaxConsecutiveFailures = 5;
        private float _backoffMultiplier = 1f;

        public EventReporter(ApmConfig config, OfflineStorage offlineStorage)
        {
            _config = config;
            _offlineStorage = offlineStorage;
            _ingestUrl = config.ServerUrl.TrimEnd('/') + "/v1/events";
        }

        /// <summary>
        /// Sends a batch of events to the server
        /// </summary>
        public void SendBatch(List<BaseEvent> events)
        {
            if (events == null || events.Count == 0)
                return;

            // Serialize events
            string json = JsonSerializer.SerializeEventBatch(events);
            byte[] data = Encoding.UTF8.GetBytes(json);

            // Compress if enabled and data is large enough
            byte[] payload = data;
            bool isCompressed = false;
            if (_config.EnableCompression && data.Length > 1024)
            {
                payload = Compress(data);
                isCompressed = true;
            }

            // Start coroutine to send
            CoroutineRunner.Instance.StartCoroutine(SendRequest(payload, isCompressed, events));
        }

        private IEnumerator SendRequest(byte[] payload, bool isCompressed, List<BaseEvent> originalEvents)
        {
            if (_isSending)
            {
                // Queue for later
                if (_offlineStorage != null)
                {
                    _offlineStorage.Store(originalEvents);
                }
                yield break;
            }

            _isSending = true;

            using (var request = new UnityWebRequest(_ingestUrl, "POST"))
            {
                request.uploadHandler = new UploadHandlerRaw(payload);
                request.downloadHandler = new DownloadHandlerBuffer();

                request.SetRequestHeader("Content-Type", "application/json");
                if (!string.IsNullOrEmpty(_config.AppKey))
                {
                    request.SetRequestHeader("X-App-Key", _config.AppKey);
                }
                if (isCompressed)
                {
                    request.SetRequestHeader("Content-Encoding", "gzip");
                }

                request.timeout = (int)_config.RequestTimeoutSeconds;

                yield return request.SendWebRequest();

                if (request.result == UnityWebRequest.Result.Success)
                {
                    OnSuccess();
                }
                else
                {
                    OnFailure(request.error, originalEvents);
                }
            }

            _isSending = false;
        }

        private void OnSuccess()
        {
            _consecutiveFailures = 0;
            _backoffMultiplier = 1f;
        }

        private void OnFailure(string error, List<BaseEvent> events)
        {
            _consecutiveFailures++;
            _backoffMultiplier = Math.Min(_backoffMultiplier * 2, 32f);

            ApmClient.Log(LogLevel.Warning, $"Failed to send events: {error}");

            // Store for retry if we haven't failed too many times
            if (_consecutiveFailures < MaxConsecutiveFailures && _offlineStorage != null)
            {
                _offlineStorage.Store(events);
            }
        }

        private byte[] Compress(byte[] data)
        {
            try
            {
                using (var output = new MemoryStream())
                {
                    using (var gzip = new GZipStream(output, CompressionMode.Compress))
                    {
                        gzip.Write(data, 0, data.Length);
                    }
                    return output.ToArray();
                }
            }
            catch (Exception ex)
            {
                ApmClient.Log(LogLevel.Warning, $"Compression failed: {ex.Message}");
                return data;
            }
        }

        /// <summary>
        /// Helper to run coroutines from non-MonoBehaviour classes
        /// </summary>
        private class CoroutineRunner : MonoBehaviour
        {
            private static CoroutineRunner _instance;

            public static CoroutineRunner Instance
            {
                get
                {
                    if (_instance == null)
                    {
                        var go = new GameObject("[OzxApm-CoroutineRunner]");
                        go.hideFlags = HideFlags.HideAndDontSave;
                        UnityEngine.Object.DontDestroyOnLoad(go);
                        _instance = go.AddComponent<CoroutineRunner>();
                    }
                    return _instance;
                }
            }
        }
    }
}

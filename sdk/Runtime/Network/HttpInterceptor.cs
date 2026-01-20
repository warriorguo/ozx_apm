using System;
using System.Collections.Generic;
using UnityEngine;
using OzxApm.Core;
using OzxApm.Models;

namespace OzxApm.Network
{
    /// <summary>
    /// Placeholder for HTTP request interception (Phase 3)
    /// </summary>
    public class HttpInterceptor
    {
        private readonly ApmConfig _config;
        private readonly ApmClient _client;
        private bool _isActive;

        public HttpInterceptor(ApmConfig config, ApmClient client)
        {
            _config = config;
            _client = client;
        }

        /// <summary>
        /// Starts HTTP interception
        /// </summary>
        public void Start()
        {
            _isActive = true;
            // Phase 3: Hook into UnityWebRequest or HttpClient
        }

        /// <summary>
        /// Stops HTTP interception
        /// </summary>
        public void Stop()
        {
            _isActive = false;
        }

        /// <summary>
        /// Manually records an HTTP request for tracking
        /// </summary>
        public void RecordRequest(
            string apiName,
            string method,
            int statusCode,
            float dnsMs,
            float tcpMs,
            float tlsMs,
            float ttfbMs,
            float downloadMs,
            long sizeBytes,
            string error = null)
        {
            if (!_isActive)
                return;

            // Phase 3: Implement HTTP event tracking
            ApmClient.Log(LogLevel.Debug, $"HTTP: {method} {apiName} -> {statusCode} ({ttfbMs + downloadMs:F0}ms)");
        }

        /// <summary>
        /// Strips sensitive parameters from URL for privacy
        /// </summary>
        public static string SanitizeUrl(string url)
        {
            if (string.IsNullOrEmpty(url))
                return url;

            try
            {
                var uri = new Uri(url);
                // Return just scheme + host + path (no query params)
                return $"{uri.Scheme}://{uri.Host}{uri.AbsolutePath}";
            }
            catch
            {
                // If parsing fails, try to strip query string manually
                int queryIndex = url.IndexOf('?');
                return queryIndex > 0 ? url.Substring(0, queryIndex) : url;
            }
        }
    }
}

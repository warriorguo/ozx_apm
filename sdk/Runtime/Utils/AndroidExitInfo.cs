using System;
using System.Collections.Generic;
using System.IO;
using UnityEngine;
using OzxApm.Core;

namespace OzxApm.Utils
{
    /// <summary>
    /// Android's record of how this app's processes ended
    /// (ActivityManager.getHistoricalProcessExitReasons, API 30+), and their
    /// traces (ApplicationExitInfo.getTraceInputStream: the ANR trace on API
    /// 30+, the native crash's tombstone on API 31+), through JNI (APM-7).
    ///
    /// Capture() runs on the main thread at Initialize (a few JNI calls: the
    /// application context and the pid); Query runs on the crash reporter's
    /// thread, attached to the JVM for the duration. Traces are copied to a
    /// file with android.os.FileUtils.copy (no byte goes through C# until the
    /// parser reads it). Every call is guarded: an exception or a missing API
    /// yields no records, never an error in the game.
    /// </summary>
    internal sealed class AndroidExitInfo : IExitInfoSource
    {
#if UNITY_ANDROID && !UNITY_EDITOR
        private AndroidJavaObject _context;
        private string _package;
        private int _sdk;
#endif

#if UNITY_ANDROID && !UNITY_EDITOR
        /// <summary>The application context (null if Capture failed).</summary>
        internal AndroidJavaObject Context => _context;
#endif

        /// <summary>The process id (0 if unknown).</summary>
        internal int Pid { get; private set; }

        /// <summary>Android API level (0 off Android).</summary>
        internal int Sdk
        {
            get
            {
#if UNITY_ANDROID && !UNITY_EDITOR
                return _sdk;
#else
                return 0;
#endif
            }
        }

        /// <summary>Main thread: the application context, package, API level and pid.</summary>
        internal static AndroidExitInfo Capture()
        {
            var info = new AndroidExitInfo();
#if UNITY_ANDROID && !UNITY_EDITOR
            try
            {
                using (var version = new AndroidJavaClass("android.os.Build$VERSION"))
                    info._sdk = version.GetStatic<int>("SDK_INT");
                using (var process = new AndroidJavaClass("android.os.Process"))
                    info.Pid = process.CallStatic<int>("myPid");
                using (var player = new AndroidJavaClass("com.unity3d.player.UnityPlayer"))
                using (var activity = player.GetStatic<AndroidJavaObject>("currentActivity"))
                {
                    if (activity != null)
                    {
                        info._context = activity.Call<AndroidJavaObject>("getApplicationContext");
                        info._package = info._context?.Call<string>("getPackageName");
                    }
                }
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("AndroidExitInfo.Capture", ex);
            }
#endif
            return info;
        }

        public List<ExitRecord> Query(ICollection<int> wantTrace, string traceDir)
        {
            var list = new List<ExitRecord>();
#if UNITY_ANDROID && !UNITY_EDITOR
            if (_sdk < 30 || _context == null || string.IsNullOrEmpty(_package))
                return list;
            bool attached = false;
            try
            {
                attached = AndroidJNI.AttachCurrentThread() == 0;
                using (var am = _context.Call<AndroidJavaObject>("getSystemService", "activity"))
                using (var infos = am?.Call<AndroidJavaObject>("getHistoricalProcessExitReasons", _package, 0, 16))
                {
                    int n = infos != null ? infos.Call<int>("size") : 0;
                    for (int i = 0; i < n; i++)
                    {
                        using (var info = infos.Call<AndroidJavaObject>("get", i))
                        {
                            if (info == null)
                                continue;
                            var r = new ExitRecord
                            {
                                Pid = info.Call<int>("getPid"),
                                TimestampMs = info.Call<long>("getTimestamp"),
                                Reason = info.Call<int>("getReason"),
                                Status = info.Call<int>("getStatus"),
                                Importance = info.Call<int>("getImportance"),
                                PssKb = info.Call<long>("getPss"),
                                RssKb = info.Call<long>("getRss"),
                                Description = info.Call<string>("getDescription") ?? "",
                            };
                            string process = info.Call<string>("getProcessName") ?? "";
                            if (process.Length > 0 && process != _package)
                                continue; // another process of the app (a service): not the game
                            // Any reason may carry a trace: "typically it'll be available
                            // when the reason is REASON_ANR, though if the process gets an
                            // ANR but still continues running and it's killed for some other
                            // reasons later, the trace will be available too"
                            // (ApplicationExitInfo.getTraceInputStream). On the A33 (Android
                            // 16) an ANR without dialog or kill: the later SIGNALED exit had
                            // it. Null when there is none (no file is created then).
                            if (wantTrace != null && wantTrace.Contains(r.Pid))
                                r.TracePath = CopyTrace(info, traceDir, r.Pid);
                            list.Add(r);
                        }
                    }
                }
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("AndroidExitInfo.Query", ex);
            }
            finally
            {
                if (attached)
                {
                    try
                    {
                        AndroidJNI.DetachCurrentThread();
                    }
                    catch
                    {
                        // Detaching is best effort.
                    }
                }
            }
#endif
            return list;
        }

#if UNITY_ANDROID && !UNITY_EDITOR
        private static string CopyTrace(AndroidJavaObject info, string dir, int pid)
        {
            string path = Path.Combine(dir, CrashReporter.TracePrefix + pid + ".bin");
            try
            {
                Directory.CreateDirectory(dir);
                using (var input = info.Call<AndroidJavaObject>("getTraceInputStream"))
                {
                    if (input == null)
                        return null;
                    try
                    {
                        using (var output = new AndroidJavaObject("java.io.FileOutputStream", path))
                        using (var utils = new AndroidJavaClass("android.os.FileUtils"))
                        {
                            utils.CallStatic<long>("copy", input, output);
                            output.Call("close");
                        }
                    }
                    finally
                    {
                        input.Call("close");
                    }
                }
                return File.Exists(path) ? path : null;
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("AndroidExitInfo.CopyTrace", ex);
                return null;
            }
        }
#endif

        /// <summary>The ABI of this process (arm64-v8a, armeabi-v7a, x86_64, x86).</summary>
        internal static string ProcessAbi()
        {
            try
            {
                switch (System.Runtime.InteropServices.RuntimeInformation.ProcessArchitecture)
                {
                    case System.Runtime.InteropServices.Architecture.Arm64: return "arm64-v8a";
                    case System.Runtime.InteropServices.Architecture.Arm: return "armeabi-v7a";
                    case System.Runtime.InteropServices.Architecture.X64: return "x86_64";
                    case System.Runtime.InteropServices.Architecture.X86: return "x86";
                }
            }
            catch
            {
                // Not available: by pointer size.
            }
            return IntPtr.Size == 8 ? "arm64-v8a" : "armeabi-v7a";
        }
    }
}

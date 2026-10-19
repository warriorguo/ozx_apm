using System;
using System.Globalization;
using System.Text;
using UnityEngine;
using OzxApm.Core;

namespace OzxApm.Utils
{
    /// <summary>
    /// The Java main thread for the ANR watchdog (APM-7), through JNI on the
    /// watchdog's thread: an android.os.Handler on the main looper without a
    /// callback (its default handleMessage does nothing), a marker message
    /// queued with sendEmptyMessage(What) and looked for with
    /// hasMessages(What). The method ids and the argument array are made once:
    /// a check allocates nothing in C# (the Java side takes a pooled Message).
    /// No C# runs on the main thread. On a block only:
    /// ActivityManager.getProcessesInErrorState and the main thread's stack
    /// (Looper.getMainLooper().getThread().getStackTrace()).
    /// Every call is guarded; a JNI exception disables the probe.
    /// </summary>
    internal sealed class AndroidMainThreadProbe : IMainThreadProbe
    {
        /// <summary>The marker's Message.what ("OZX").</summary>
        internal const int What = 0x4F5A58;
        private const int NotResponding = 2; // ActivityManager.ProcessErrorStateInfo.NOT_RESPONDING

#if UNITY_ANDROID && !UNITY_EDITOR
        private readonly AndroidJavaObject _context;
        private readonly int _pid;
        private readonly jvalue[] _what = new jvalue[1];
        private IntPtr _handler;
        private IntPtr _send, _has, _remove;
        private bool _attached;
        private bool _broken;
        private AndroidJavaObject _am;
#endif

        internal AndroidMainThreadProbe(AndroidJavaObject context, int pid)
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            _context = context;
            _pid = pid;
#endif
        }

        public bool Start()
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            if (_context == null || _pid <= 0)
                return false;
            try
            {
                _attached = AndroidJNI.AttachCurrentThread() == 0;
                IntPtr looperClass = AndroidJNI.FindClass("android/os/Looper");
                IntPtr handlerClass = AndroidJNI.FindClass("android/os/Handler");
                if (Failed() || looperClass == IntPtr.Zero || handlerClass == IntPtr.Zero)
                    return false;
                IntPtr getMain = AndroidJNI.GetStaticMethodID(looperClass, "getMainLooper", "()Landroid/os/Looper;");
                IntPtr looper = AndroidJNI.CallStaticObjectMethod(looperClass, getMain, new jvalue[0]);
                IntPtr ctor = AndroidJNI.GetMethodID(handlerClass, "<init>", "(Landroid/os/Looper;)V");
                if (Failed() || looper == IntPtr.Zero)
                    return false;
                var args = new jvalue[1];
                args[0].l = looper;
                IntPtr handler = AndroidJNI.NewObject(handlerClass, ctor, args);
                if (Failed() || handler == IntPtr.Zero)
                    return false;
                _handler = AndroidJNI.NewGlobalRef(handler);
                _send = AndroidJNI.GetMethodID(handlerClass, "sendEmptyMessage", "(I)Z");
                _has = AndroidJNI.GetMethodID(handlerClass, "hasMessages", "(I)Z");
                _remove = AndroidJNI.GetMethodID(handlerClass, "removeMessages", "(I)V");
                AndroidJNI.DeleteLocalRef(handler);
                AndroidJNI.DeleteLocalRef(looper);
                AndroidJNI.DeleteLocalRef(looperClass);
                AndroidJNI.DeleteLocalRef(handlerClass);
                _what[0].i = What;
                if (Failed() || _send == IntPtr.Zero || _has == IntPtr.Zero || _remove == IntPtr.Zero)
                    return false;
                _am = _context.Call<AndroidJavaObject>("getSystemService", "activity");
                return _am != null;
            }
            catch (Exception ex)
            {
                _broken = true;
                ApmClient.ReportInternalError("AndroidMainThreadProbe.Start", ex);
                return false;
            }
#else
            return false;
#endif
        }

        public void Stop()
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            try
            {
                if (_handler != IntPtr.Zero)
                {
                    if (!_broken)
                        AndroidJNI.CallVoidMethod(_handler, _remove, _what);
                    Failed();
                    AndroidJNI.DeleteGlobalRef(_handler);
                    _handler = IntPtr.Zero;
                }
                _am?.Dispose();
                _am = null;
            }
            catch
            {
                // Releasing is best effort.
            }
            if (_attached)
            {
                _attached = false;
                try
                {
                    AndroidJNI.DetachCurrentThread();
                }
                catch
                {
                    // Detaching is best effort.
                }
            }
#endif
        }

        public bool Post()
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            if (_broken || _handler == IntPtr.Zero)
                return false;
            bool ok = AndroidJNI.CallBooleanMethod(_handler, _send, _what);
            return !Failed() && ok;
#else
            return false;
#endif
        }

        public bool IsPending()
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            if (_broken || _handler == IntPtr.Zero)
                return false;
            bool pending = AndroidJNI.CallBooleanMethod(_handler, _has, _what);
            return !Failed() && pending;
#else
            return false;
#endif
        }

        public void Cancel()
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            if (_broken || _handler == IntPtr.Zero)
                return;
            AndroidJNI.CallVoidMethod(_handler, _remove, _what);
            Failed();
#endif
        }

        public bool IsNotResponding(out string message)
        {
            message = "";
#if UNITY_ANDROID && !UNITY_EDITOR
            if (_broken || _am == null)
                return false;
            try
            {
                using (var list = _am.Call<AndroidJavaObject>("getProcessesInErrorState"))
                {
                    int n = list != null ? list.Call<int>("size") : 0;
                    for (int i = 0; i < n && i < 32; i++)
                    {
                        using (var info = list.Call<AndroidJavaObject>("get", i))
                        {
                            if (info == null || info.Get<int>("pid") != _pid || info.Get<int>("condition") != NotResponding)
                                continue;
                            message = CrashTraceParser.Cut(info.Get<string>("shortMsg") ?? "", 300);
                            return true;
                        }
                    }
                }
            }
            catch (Exception ex)
            {
                _broken = true;
                ApmClient.ReportInternalError("AndroidMainThreadProbe.IsNotResponding", ex);
            }
#endif
            return false;
        }

        public string MainThreadStack(int maxFrames)
        {
#if UNITY_ANDROID && !UNITY_EDITOR
            try
            {
                var sb = new StringBuilder(2048);
                sb.Append("Thread \"main\" tid=").Append(_pid.ToString(CultureInfo.InvariantCulture)).Append(" crashed\n");
                using (var looperClass = new AndroidJavaClass("android.os.Looper"))
                using (var looper = looperClass.CallStatic<AndroidJavaObject>("getMainLooper"))
                using (var thread = looper.Call<AndroidJavaObject>("getThread"))
                {
                    var frames = thread.Call<AndroidJavaObject[]>("getStackTrace");
                    if (frames != null)
                    {
                        for (int i = 0; i < frames.Length; i++)
                        {
                            using (var f = frames[i])
                            {
                                if (i < maxFrames && f != null)
                                    sb.Append("at ").Append(CrashTraceParser.Cut(f.Call<string>("toString") ?? "", 300).Replace('\n', ' ')).Append('\n');
                            }
                        }
                    }
                }
                return CrashTraceParser.Cut(sb.ToString(), CrashTraceParser.MaxStackChars);
            }
            catch (Exception ex)
            {
                ApmClient.ReportInternalError("AndroidMainThreadProbe.MainThreadStack", ex);
            }
#endif
            return "";
        }

#if UNITY_ANDROID && !UNITY_EDITOR
        // A pending Java exception: cleared, and the probe disabled.
        private bool Failed()
        {
            IntPtr ex = AndroidJNI.ExceptionOccurred();
            if (ex == IntPtr.Zero)
                return false;
            AndroidJNI.ExceptionClear();
            AndroidJNI.DeleteLocalRef(ex);
            _broken = true;
            return true;
        }
#endif
    }
}

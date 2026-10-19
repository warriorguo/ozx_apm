package fingerprint

// Generated from the A33 device run (APM-7 round 2): one ANR (anrui=25 with taps),
// reported live by the watchdog and again from the exit record's ANR trace.

// anr-84ed4f3bf9c36bc9: details.cause 'ANR Input dispatching timed out (925aad com.ozx.apmtest/com.unity3d.player.UnityPlayerActivity is not responding. Waited 10000ms for MotionEvent).'
const anrWatchdogStack = `Thread "main" tid=12318 crashed
at java.lang.Thread.sleep(Native Method)
at java.lang.Thread.sleep(Thread.java:451)
at java.lang.Thread.sleep(Thread.java:356)
at com.unity3d.player.ReflectionHelper.nativeProxyInvoke(Native Method)
at com.unity3d.player.ReflectionHelper.-$$Nest$smnativeProxyInvoke(Unknown Source:0)
at com.unity3d.player.L.invoke(Unknown Source:22)
at java.lang.reflect.Proxy.invoke(Proxy.java:1006)
at $Proxy4.run(Unknown Source)
at android.os.Handler.handleCallback(Handler.java:995)
at android.os.Handler.dispatchMessage(Handler.java:103)
at android.os.Looper.loopOnce(Looper.java:273)
at android.os.Looper.loop(Looper.java:363)
at android.app.ActivityThread.main(ActivityThread.java:10060)
at java.lang.reflect.Method.invoke(Native Method)
at com.android.internal.os.RuntimeInit$MethodAndArgsCaller.run(RuntimeInit.java:632)
at com.android.internal.os.ZygoteInit.main(ZygoteInit.java:975)
`

const anrWatchdogStackCause = "ANR Input dispatching timed out (925aad com.ozx.apmtest/com.unity3d.player.UnityPlayerActivity is not responding. Waited 10000ms for MotionEvent)."

// anr-0a43afdde482d357: details.cause 'Input dispatching timed out (925aad com.ozx.apmtest/com.unity3d.player.UnityPlayerActivity is not responding. Waited 10000ms for MotionEvent).'
const anrExitTraceStack = `Thread "main" tid=12318 crashed
at java.lang.Thread.sleep(Native method)
at java.lang.Thread.sleep(Thread.java:451)
at java.lang.Thread.sleep(Thread.java:356)
at com.unity3d.player.ReflectionHelper.nativeProxyInvoke(Native method)
at com.unity3d.player.ReflectionHelper.-$$Nest$smnativeProxyInvoke(unavailable:0)
at com.unity3d.player.L.invoke(unavailable:22)
at java.lang.reflect.Proxy.invoke(Proxy.java:1006)
at java.lang.Runnable.run(Runnable.java:-2)
at android.os.Handler.handleCallback(Handler.java:995)
at android.os.Handler.dispatchMessage(Handler.java:103)
at android.os.Looper.loopOnce(Looper.java:273)
at android.os.Looper.loop(Looper.java:363)
at android.app.ActivityThread.main(ActivityThread.java:10060)
at java.lang.reflect.Method.invoke(Native method)
at com.android.internal.os.RuntimeInit$MethodAndArgsCaller.run(RuntimeInit.java:632)
at com.android.internal.os.ZygoteInit.main(ZygoteInit.java:975)
Thread "UnityMain" tid=12334
#00 pc 000000000009371c /apex/com.android.runtime/lib64/bionic/libc.so (syscall+28) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)
#01 pc 0000000000070a3c /apex/com.android.runtime/lib64/bionic/libc.so (__futex_wait_ex+144) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)
#02 pc 000000000007dbdc /apex/com.android.runtime/lib64/bionic/libc.so (pthread_cond_wait+72) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)
#03 pc 00000000004624ec libunity.so (???) (BuildId: aa90508360cec411)
#04 pc 0000000000440330 libunity.so (???) (BuildId: aa90508360cec411)
#05 pc 00000000003256a8 libunity.so (???) (BuildId: aa90508360cec411)
#06 pc 000000000031e09c libunity.so (???) (BuildId: aa90508360cec411)
#07 pc 0000000000458c38 libunity.so (???) (BuildId: aa90508360cec411)
#08 pc 000000000046d4f4 libunity.so (???) (BuildId: aa90508360cec411)
at com.unity3d.player.UnityPlayer.nativeRender(Native method)
at com.unity3d.player.UnityPlayer.-$$Nest$mnativeRender(unavailable:0)
at com.unity3d.player.T0.handleMessage(unavailable:122)
at android.os.Handler.dispatchMessage(Handler.java:106)
at android.os.Looper.loopOnce(Looper.java:273)
at android.os.Looper.loop(Looper.java:363)
at com.unity3d.player.U0.run(unavailable:24)
`

const anrExitTraceStackCause = "Input dispatching timed out (925aad com.ozx.apmtest/com.unity3d.player.UnityPlayerActivity is not responding. Waited 10000ms for MotionEvent)."

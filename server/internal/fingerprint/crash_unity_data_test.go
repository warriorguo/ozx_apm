package fingerprint

// Generated from the A33 device logcat (APM-7 run 21:40:51, ForceCrash AccessViolation):
// Unity's "E CRASH" lines (its tombstone_NN file) and the system tombstone ("F DEBUG"),
// in the SDK's normalized form (install paths reduced to the file name).

const unityTombstoneStack = `Thread "UnityMain" tid=32679 crashed
#00 pc 00000000000d0148 /apex/com.android.runtime/lib64/bionic/libc.so (tgkill+8) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)
#01 pc 000000000023f7c0 libunity.so (BuildId: a5f31388f37437dc)
#02 pc 000000000056a030 libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#03 pc 000000000056542c libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#04 pc 00000000004a41d8 libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#05 pc 00000000004a4124 libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#06 pc 00000000003cd1e8 libunity.so (BuildId: a5f31388f37437dc)
#07 pc 00000000003db160 libunity.so (BuildId: a5f31388f37437dc)
#08 pc 00000000003e3314 libunity.so (BuildId: a5f31388f37437dc)
#09 pc 000000000028ce38 libunity.so (BuildId: a5f31388f37437dc)
#10 pc 000000000031e3ec libunity.so (BuildId: a5f31388f37437dc)
#11 pc 000000000031e42c libunity.so (BuildId: a5f31388f37437dc)
#12 pc 000000000031e6d8 libunity.so (BuildId: a5f31388f37437dc)
#13 pc 0000000000459178 libunity.so (BuildId: a5f31388f37437dc)
#14 pc 000000000046d9f4 libunity.so (BuildId: a5f31388f37437dc)
#15 pc 00000000002d1160 /system/framework/arm64/boot.oat (BuildId: 29e0526702d2b52405ceed8866d8685ba6dc2e28)
`

const systemTombstoneStack = `Thread "UnityMain" tid=32679 crashed
#00 pc 00000000000d0148 /apex/com.android.runtime/lib64/bionic/libc.so (tgkill+8) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)
#01 pc 000000000023f7c0 libunity.so (BuildId: a5f31388f37437dc)
#02 pc 000000000056a030 libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#03 pc 000000000056542c libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#04 pc 00000000004a41d8 libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#05 pc 00000000004a4124 libil2cpp.so (BuildId: 96f86f45846548bd6b2d1f50893e95ab4a9408a2)
#06 pc 00000000003cd1e8 libunity.so (BuildId: a5f31388f37437dc)
#07 pc 00000000003db160 libunity.so (BuildId: a5f31388f37437dc)
#08 pc 00000000003e3314 libunity.so (BuildId: a5f31388f37437dc)
#09 pc 000000000028ce38 libunity.so (BuildId: a5f31388f37437dc)
#10 pc 000000000031e3ec libunity.so (BuildId: a5f31388f37437dc)
#11 pc 000000000031e42c libunity.so (BuildId: a5f31388f37437dc)
#12 pc 000000000031e6d8 libunity.so (BuildId: a5f31388f37437dc)
#13 pc 0000000000459178 libunity.so (BuildId: a5f31388f37437dc)
#14 pc 000000000046d9f4 libunity.so (BuildId: a5f31388f37437dc)
#15 pc 00000000002d1160 /system/framework/arm64/boot.oat (art_jni_trampoline+112) (BuildId: 29e0526702d2b52405ceed8866d8685ba6dc2e28)
#16 pc 000000000068a120 /apex/com.android.art/lib64/libart.so (nterp_helper+4016) (BuildId: 80d2ab18f9d259d8e546c1e6bae752b1)
#17 pc 000000000000d24c base.vdex (com.unity3d.player.UnityPlayer.-$$Nest$mnativeRender+0)
#18 pc 00000000006891a4 /apex/com.android.art/lib64/libart.so (nterp_helper+52) (BuildId: 80d2ab18f9d259d8e546c1e6bae752b1)
#19 pc 000000000000c5bc base.vdex (com.unity3d.player.T0.handleMessage+244)
#20 pc 000000000050db5c /system/framework/arm64/boot-framework.oat (android.os.Handler.dispatchMessage+124) (BuildId: ff0e8ca353a98dc71bb2a50e1099c11a52ae7a76)
#21 pc 000000000055411c /system/framework/arm64/boot-framework.oat (android.os.Looper.loopOnce+1932) (BuildId: ff0e8ca353a98dc71bb2a50e1099c11a52ae7a76)
#22 pc 0000000000553914 /system/framework/arm64/boot-framework.oat (android.os.Looper.loop+244) (BuildId: ff0e8ca353a98dc71bb2a50e1099c11a52ae7a76)
#23 pc 0000000000689208 /apex/com.android.art/lib64/libart.so (nterp_helper+152) (BuildId: 80d2ab18f9d259d8e546c1e6bae752b1)
#24 pc 000000000000c84c base.vdex (com.unity3d.player.U0.run+48)
#25 pc 000000000032d194 /apex/com.android.art/lib64/libart.so (art_quick_invoke_stub+612) (BuildId: 80d2ab18f9d259d8e546c1e6bae752b1)
#26 pc 00000000002de270 /apex/com.android.art/lib64/libart.so (art::ArtMethod::Invoke(art::Thread*, unsigned int*, unsigned int, art::JValue*, char const*)
#27 pc 00000000004bfcf4 /apex/com.android.art/lib64/libart.so (art::Thread::CreateCallback(void*)
#28 pc 00000000004bf940 /apex/com.android.art/lib64/libart.so (art::Thread::CreateCallbackWithUffdGc(void*)
#29 pc 000000000007e8ac /apex/com.android.runtime/lib64/bionic/libc.so (__pthread_start(void*)
#30 pc 0000000000071bdc /apex/com.android.runtime/lib64/bionic/libc.so (__start_thread+64) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)
Thread "main" tid=32664
#00 pc 00000000000c4d28 /apex/com.android.runtime/lib64/bionic/libc.so (__epoll_pwait+8) (BuildId: fa6ae44f4b2b6a34abebf1b3983cbf88)
`

// Test input for internal/symbols: inlined code in IL2CPP's output, shaped
// like the S1 device crash (APM-7). UpdateApm7's `a?.Invoke()` calls
// Action_Invoke_..._inline, which clang inlines; its body is further down
// the file, below SessionManager.get_SessionId's "//<source_info>"
// comment, so its DWARF line rows map to SessionManager.cs:30 unless the
// call site is used. CompleteUpload calls an inlined NullCheck after its
// prologue; get_Count starts with one (no prologue: the declaration row at
// its entry is replaced by the call site). gen.sh
// compiles it like demo.cpp. Do not reformat: the tests use its line numbers.
#include <stdint.h>

extern "C" {

typedef void (*Fn)(void*);
struct Action_t { Fn fn; void* target; };
struct Batch_t { int32_t count; };

static inline __attribute__((always_inline)) void Action_Invoke_m7126A54D_inline(Action_t* __this);
static inline __attribute__((always_inline)) void NullCheck(void* p);
static inline __attribute__((always_inline)) void NullCheckTrap(void* p);
__attribute__((noinline, noreturn)) void Il2CppCodeGen_RaiseNullReference_m00000001(void);

__attribute__((noinline)) void ApmTestDriver_UpdateApm7_mAC92D61E(void* __this, Action_t* a, float dt)
{
	volatile float t = dt;
	//<source_info:/Users/builder/ozx/Assets/Scripts/ApmTestDriver.cs:1087>
	t = t * 2;
	//<source_info:/Users/builder/ozx/Assets/Scripts/ApmTestDriver.cs:1088>
	Action_Invoke_m7126A54D_inline(a);
	//<source_info:/Users/builder/ozx/Assets/Scripts/ApmTestDriver.cs:1090>
	t = t + 1;
}

__attribute__((noinline)) int32_t OfflineStorage_CompleteUpload_m23B6F0BC(void* __this, Batch_t* batch)
{
	//<source_info:/Users/builder/sdk/Runtime/Network/OfflineStorage.cs:558>
	NullCheck(batch);
	return batch->count + 1;
}

__attribute__((noinline)) int32_t OfflineStorage_get_Count_m5A6B7C8D(Batch_t* batch)
{
	//<source_info:/Users/builder/sdk/Runtime/Network/OfflineStorage.cs:600>
	NullCheckTrap(batch);
	return batch->count;
}

__attribute__((noinline, noreturn)) void Il2CppCodeGen_RaiseNullReference_m00000001(void)
{
	volatile int* p = (volatile int*)0;
	*p = 1;
	__builtin_trap();
}

__attribute__((noinline)) int32_t SessionManager_get_SessionId_m2E1B3C4D(void* __this)
{
	//<source_info:/Users/builder/sdk/Runtime/Core/SessionManager.cs:30>
	return *(volatile int32_t*)__this;
}

static inline __attribute__((always_inline)) void Action_Invoke_m7126A54D_inline(Action_t* __this)
{
	Fn f = __this->fn;
	f(__this->target);
}

static inline __attribute__((always_inline)) void NullCheck(void* p)
{
	if (p == 0)
		Il2CppCodeGen_RaiseNullReference_m00000001();
}

static inline __attribute__((always_inline)) void NullCheckTrap(void* p)
{
	if (p == 0)
		__builtin_trap();
}

}

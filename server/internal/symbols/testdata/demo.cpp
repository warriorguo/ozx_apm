// Test input for internal/symbols: code shaped like IL2CPP's output (C
// functions named <Type>_<Method>_m<hash>, "//<source_info:...>" comments
// before each C# statement, as written with --emit-source-mapping), and one
// C++ runtime function. gen.sh compiles it for arm64 and arm (Thumb) with
// debug information and writes LineNumberMappings.json the way IL2CPP's
// SymbolsCollector does. Do not reformat: the tests use its line numbers.
#include <stdint.h>

namespace il2cpp { namespace vm {
struct Exception {
	__attribute__((noinline)) static void Raise(void* ex);
};
void Exception::Raise(void* ex)
{
	volatile int* p = (volatile int*)ex;
	*p = 1;
}
} }

extern "C" {

__attribute__((noinline)) int32_t Helper_Twice_m0A1B2C3D(int32_t x)
{
	volatile int32_t v = x;
	//<source_info:/Users/builder/ozx/Assets/Scripts/Util/Helper.cs:12>
	v = v * 2;
	//<source_info:/Users/builder/ozx/Assets/Scripts/Util/Helper.cs:13>
	return v;
}

__attribute__((noinline)) int32_t RoomLoader_LoadTemplate_m6F444615B7A27593A26813DB3252F8AA1ADBD2E1(void* __this, int32_t roomId)
{
	volatile int32_t result = 0;
	//<source_info:/Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs:70>
	result = roomId * 3;
	//<source_info:/Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs:71>
	result = Helper_Twice_m0A1B2C3D(result);
	//<source_info:/Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs:72>
	if (result > 100)
	{
		il2cpp::vm::Exception::Raise(__this);
	}
	//<source_info:/Users/builder/ozx/Assets/Scripts/Rooms/RoomLoader.cs:74>
	return result + 1;
}

__attribute__((noinline)) void U3CU3Ec_U3CStartU3Eb__5_0_m11223344(void* __this)
{
	//<source_info:/Users/builder/ozx/Library/PackageCache/com.ozx.apm@2.0.0/Runtime/Core/ApmClient.cs:500>
	RoomLoader_LoadTemplate_m6F444615B7A27593A26813DB3252F8AA1ADBD2E1(__this, 7);
}

__attribute__((noinline)) int32_t List_1_get_Item_m9C2F2E2C2B0D0B6A(void* __this, int32_t i)
{
	return i + 1;
}

}

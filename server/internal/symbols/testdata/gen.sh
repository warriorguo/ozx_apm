#!/bin/bash
# Regenerates the test libraries from demo.cpp with the Android NDK that ships
# with Unity, and LineNumberMappings.json / MethodMap.tsv the way IL2CPP
# writes them. Run from this directory:
#   NDK=/Applications/Unity/Hub/Editor/2022.3.62f3/PlaybackEngines/AndroidPlayer/NDK ./gen.sh
set -euo pipefail
BIN=$NDK/toolchains/llvm/prebuilt/darwin-x86_64/bin
# The source path is what the debug information records: use the build
# machine path that IL2CPP would have used.
SRC=/Users/builder/ozx/Library/Bee/artifacts/Android/il2cppOutput/cpp/Assembly-CSharp.cpp
mkdir -p "$(dirname "$SRC")" 2>/dev/null || true
FLAGS="-g -O1 -shared -fPIC -fno-exceptions -Wl,--build-id=sha1 -ffile-prefix-map=$PWD/=/Users/builder/ozx/Library/Bee/artifacts/Android/il2cppOutput/cpp/"
cp demo.cpp Assembly-CSharp.cpp
"$BIN/clang++" --target=aarch64-linux-android23 $FLAGS -o libdemo-arm64.elf Assembly-CSharp.cpp
"$BIN/clang++" --target=armv7a-linux-androideabi23 -mthumb $FLAGS -o libdemo-arm.elf Assembly-CSharp.cpp
"$BIN/llvm-objcopy" --strip-all libdemo-arm64.elf libdemo-arm64-stripped.elf
rm Assembly-CSharp.cpp
# LineNumberMappings.json: IL2CPP's format (SymbolsMetadataContainer.SerializeToJson),
# one entry per "//<source_info:File:Line>" comment, keyed by the comment's line.
python3 - "$SRC" <<'EOF'
import re, sys, json
cpp = sys.argv[1]
pat = re.compile(r'\t*//<source_info:(.+):(\d+)>')
m = {}
for n, line in enumerate(open('demo.cpp'), 1):
    g = pat.match(line)
    if g:
        m.setdefault(g.group(1), {})[str(n)] = int(g.group(2))
out = ['{', '"%s" : {' % cpp.replace('\\', '\\\\')]
files = []
for cs, lines in m.items():
    body = ',\n'.join('"%s" : %d' % (k, v) for k, v in lines.items())
    files.append('"%s" : {\n%s\n}' % (cs.replace('\\', '\\\\'), body))
out.append(',\n'.join(files))
out.append('}')
out.append('}')
open('LineNumberMappings.json', 'w').write('\n'.join(out) + '\n')
EOF
printf 'RoomLoader_LoadTemplate_m6F444615B7A27593A26813DB3252F8AA1ADBD2E1\tSystem.Int32 Game.Rooms.RoomLoader::LoadTemplate(System.Object,System.Int32)\tAssembly-CSharp\nU3CU3Ec_U3CStartU3Eb__5_0_m11223344\tSystem.Void OzxApm.Core.ApmClient/<>c::<Start>b__5_0()\tOzxApm\n' > MethodMap.tsv
echo ok

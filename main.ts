import {spawn} from 'node:child_process';
import {fileURLToPath} from 'node:url';
import {dirname, join} from 'node:path';
// The forge is a statically linked Go binary committed beside this launcher.
// It binds an ephemeral port, registers with MAKEFUN_ENV_URL, serves the suite,
// and exits 0 once the suite reports completion; this launcher mirrors its exit.
const dir=dirname(fileURLToPath(import.meta.url));
const child=spawn(join(dir,'forge'),[],{stdio:'inherit',env:process.env});
child.on('error',()=>process.exit(1));
child.on('exit',(code)=>process.exit(code??0));

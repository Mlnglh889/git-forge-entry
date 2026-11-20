import {createApp} from './app.ts';
const env=(process.env.MAKEFUN_ENV_URL??'').replace(/\/+$/,'');
if(!env){process.exit(1)}
const server=createApp();
await new Promise<void>((resolve,reject)=>{server.once('error',reject);server.listen(0,()=>resolve())});
const address=server.address();
if(!address||typeof address==='string')throw new Error('no port');
const request=async(url:string,init:RequestInit={})=>fetch(url,{...init,signal:AbortSignal.timeout(30000)});
for(;;){try{const r=await request(`${env}/register`,{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({port:address.port})});if(r.status===200||r.status===409)break}catch{}await new Promise(r=>setTimeout(r,100))}
for(;;){try{const r=await request(`${env}/done`);if(r.ok&&(await r.json()).done===true)break}catch{}await new Promise(r=>setTimeout(r,100))}
server.closeAllConnections();server.close();

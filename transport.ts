import {createServer,type Socket} from 'node:net';
// Minimal HTTP/1.1 transport over raw sockets. The suite's only clients are undici
// fetch and the kit git-client: HTTP/1.1, content-length (or chunked) bodies,
// keep-alive. Serving them directly avoids node:http's per-request machinery.
export type Req={method:string,url:string,headers:Record<string,string>,body:Buffer};
export type Reply={status:number,body:Buffer,headers?:Record<string,string|number>};
const EMPTY=Buffer.alloc(0);
const STATUS:Record<number,string>={200:'OK',201:'Created',202:'Accepted',204:'No Content',400:'Bad Request',401:'Unauthorized',404:'Not Found',405:'Method Not Allowed',409:'Conflict',413:'Payload Too Large',422:'Unprocessable Entity',431:'Request Header Fields Too Large',500:'Internal Server Error',501:'Not Implemented'};
const MAX_BODY=32*1024*1024,MAX_HEAD=64*1024;
export function createHttpServer(handle:(req:Req)=>Reply){
 let requestId=0;
 const sockets=new Set<Socket>();
 const server=createServer({noDelay:true,allowHalfOpen:false},(socket:Socket)=>{
  sockets.add(socket);socket.on('close',()=>sockets.delete(socket));
  let buf=EMPTY,busy=false,closed=false;
  let pending:null|{method:string,url:string,headers:Record<string,string>,need:number,chunked:boolean,body:Buffer[],bytes:number}=null;
  socket.setTimeout(20000,()=>socket.destroy());
  socket.on('error',()=>socket.destroy());
  const fail=(status:number)=>{if(closed)return;closed=true;const b=Buffer.from(JSON.stringify({message:STATUS[status]||'Error'})+'\n');socket.end(Buffer.concat([Buffer.from(`HTTP/1.1 ${status} ${STATUS[status]||'Error'}\r\ncontent-type: application/json; charset=utf-8\r\ncontent-length: ${b.length}\r\nconnection: close\r\n\r\n`,'latin1'),b]))};
  const serialize=(r:Reply,close:boolean,head:boolean)=>{const h=r.headers||{};let s=`HTTP/1.1 ${r.status} ${STATUS[r.status]||'Status'}\r\ncontent-type: ${h['content-type']??'application/json; charset=utf-8'}\r\ncontent-length: ${r.body.length}\r\nx-github-request-id: ${++requestId}\r\n`;for(const k of Object.keys(h))if(k!=='content-type'&&k!=='content-length')s+=`${k}: ${h[k]}\r\n`;s+=close?'connection: close\r\n\r\n':'connection: keep-alive\r\n\r\n';const headBuf=Buffer.from(s,'latin1');return head?headBuf:Buffer.concat([headBuf,r.body])};
  const finish=(p:NonNullable<typeof pending>)=>{const body=p.body.length===1?p.body[0]:p.body.length?Buffer.concat(p.body):EMPTY;let reply:Reply;try{reply=handle({method:p.method,url:p.url,headers:p.headers,body})}catch(e){reply={status:500,body:Buffer.from(JSON.stringify({message:e instanceof Error?e.message:'Internal server error'})+'\n')}}const conn=(p.headers['connection']||'').toLowerCase();const close=conn==='close'||conn!=='keep-alive'&&p.headers['x-http-version']==='1.0';const out=serialize(reply,close,p.method==='HEAD');pending=null;if(close){closed=true;socket.end(out);busy=true;return}if(!socket.write(out)){busy=true;socket.once('drain',()=>{busy=false;drive()})}};
  const drive=()=>{while(!busy&&!closed){if(!pending){const idx=buf.indexOf('\r\n\r\n');if(idx<0){if(buf.length>MAX_HEAD)fail(431);return}const head=buf.subarray(0,idx).toString('latin1');buf=buf.subarray(idx+4);const lines=head.split('\r\n');const sp1=lines[0].indexOf(' '),sp2=lines[0].indexOf(' ',sp1+1);if(sp1<0){fail(400);return}const method=lines[0].slice(0,sp1),url=sp2<0?lines[0].slice(sp1+1):lines[0].slice(sp1+1,sp2),version=sp2<0?'':lines[0].slice(sp2+1);if(!method||!url){fail(400);return}const headers:Record<string,string>={'x-http-version':version.includes('1.0')?'1.0':'1.1'};for(let i=1;i<lines.length;i++){const c=lines[i].indexOf(':');if(c>0)headers[lines[i].slice(0,c).trim().toLowerCase()]=lines[i].slice(c+1).trim()}if((headers['expect']||'').toLowerCase().includes('100-continue'))socket.write('HTTP/1.1 100 Continue\r\n\r\n');const chunked=/chunked/i.test(headers['transfer-encoding']||'');const need=chunked?-1:parseInt(headers['content-length']||'0',10)||0;if(need>MAX_BODY){fail(413);return}pending={method,url,headers,need,chunked,body:[],bytes:0}}
   const p=pending;
   if(p.chunked){for(;;){const nl=buf.indexOf('\r\n');if(nl<0)return;const sizeLine=buf.subarray(0,nl).toString('latin1');const semi=sizeLine.indexOf(';');const size=parseInt(semi<0?sizeLine:sizeLine.slice(0,semi),16);if(!Number.isFinite(size)||size<0){fail(400);return}if(size===0){const term=buf.indexOf('\r\n\r\n');if(term<0)return;buf=buf.subarray(term+4);finish(p);break}if(buf.length<nl+2+size+2)return;p.bytes+=size;if(p.bytes>MAX_BODY){fail(413);return}p.body.push(buf.subarray(nl+2,nl+2+size));buf=buf.subarray(nl+2+size+2)}}
   else{if(buf.length<p.need)return;if(p.need>0){p.body.push(buf.subarray(0,p.need));buf=buf.subarray(p.need)}finish(p)}}};
  socket.on('data',d=>{if(closed)return;buf=buf.length?Buffer.concat([buf,d]):d;drive()});
 });
 (server as any).closeAllConnections=()=>{for(const s of sockets)s.destroy()};
 return server;
}

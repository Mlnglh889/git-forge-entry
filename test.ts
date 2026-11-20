import {test} from 'node:test';
import assert from 'node:assert/strict';
import net from 'node:net';
import {GitStore,pack,unpack,objectId,packet,FLUSH,gitResponse,ZERO} from './git.ts';
import {Forge} from './store.ts';
import {execute} from './graphql.ts';
import {createApp} from './app.ts';
test('pack round trip preserves arbitrary binary objects and rejects corruption',()=>{
 const source=new GitStore();const id=source.put('blob',Buffer.from([0,255,128,10,1]));const encoded=pack([...source.objects.values()]);const target=new Map();unpack(encoded,target);assert.deepEqual(target.get(id)?.data,source.objects.get(id)?.data);const corrupt=Buffer.from(encoded);corrupt[15]^=1;assert.throws(()=>unpack(corrupt,new Map()),/checksum/);
});
test('mixed receive-pack applies a fast-forward and refuses a divergent ref',()=>{
 const s=new GitStore();const blob=s.put('blob',Buffer.from('first'));const files=new Map([['a',{oid:blob,mode:'100644'}]]);const root=s.commit(files,[],'root');const next=s.commit(files,[root],'next');const divergent=s.commit(files,[],'other root');s.refs.set('refs/heads/main',root);s.refs.set('refs/heads/topic',root);
 const input=Buffer.concat([packet(`${root} ${next} refs/heads/main\0report-status\n`),packet(`${root} ${divergent} refs/heads/topic\n`),FLUSH,pack([])]);const response=gitResponse(s,'git-receive-pack',false,false,input).toString();assert.match(response,/ok refs\/heads\/main/);assert.match(response,/ng refs\/heads\/topic non-fast-forward/);assert.equal(s.refs.get('refs/heads/main'),next);assert.equal(s.refs.get('refs/heads/topic'),root);
});
test('shallow clone sends one generation then deepen supplies the parent',()=>{
 const s=new GitStore(),files=new Map();const root=s.commit(files,[],'root'),tip=s.commit(files,[root],'tip');const input=Buffer.concat([packet('command=fetch\n'),Buffer.from('0001'),packet(`want ${tip}\n`),packet(`have ${tip}\n`),packet(`shallow ${tip}\n`),packet('deepen 2\n'),FLUSH]);const response=gitResponse(s,'git-upload-pack',false,true,input);assert.ok(response.includes(Buffer.from(`unshallow ${tip}`)));assert.ok(response.includes(Buffer.from('packfile\n')));
});
test('GraphQL aliases, variables, fragments, and stable comments share REST state',()=>{
 const f=new Forge();f.bind('alice');const r=f.create({name:'project'});const i=r.issue({title:'A real issue'});const response=execute(f,{query:'mutation ArbitraryName($input: AddCommentInput!){ posted:addComment(input:$input){commentEdge{node{id body}}}}',variables:{input:{subjectId:i.node_id,body:'Hello'}}});assert.equal(response.errors,undefined);assert.equal(typeof response.data.posted.commentEdge.node.id,'string');assert.equal(i._comments[0].body,'Hello');const read=execute(f,{query:'query { value:repository(owner:"alice",name:"project"){ ...Repo } } fragment Repo on Repository { id name }'});assert.equal(read.data.value.name,'project');const unknown=execute(f,{query:'{viewer{noSuchField}}'});assert.match(unknown.errors[0].message,/noSuchField/);
});
test('MCP invalid arguments cannot crash server; malformed JSON has parse error',async()=>{
 const server=createApp();await new Promise<void>(resolve=>server.listen(0,'127.0.0.1',resolve));const address=server.address() as any;const base=`http://127.0.0.1:${address.port}`;try{
 const invoke=(body:string)=>fetch(base+'/mcp',{method:'POST',headers:{authorization:'Bearer '+(process.env.MAKEFUN_SHOP_TOKEN||'test-token'),'content-type':'application/json'},body});const missing=await invoke(JSON.stringify({jsonrpc:'2.0',id:9,method:'tools/call',params:{name:'create_issue',arguments:{}}}));const m=await missing.json();assert.equal(m.id,9);assert.equal(m.error.code,-32602);const invalid=await invoke('{broken');assert.equal(invalid.status,400);assert.equal((await invalid.json()).error.code,-32700);assert.equal((await fetch(base+'/healthz')).status,200);
 }finally{server.closeAllConnections();await new Promise<void>(resolve=>server.close(()=>resolve()))}
});
test('early-exit fast-forward reachability walks only to the old tip',()=>{
 const s=new GitStore(),files=new Map();let tip=s.commit(files,[],'c0');const ids=[tip];for(let i=1;i<50;i++){tip=s.commit(files,[tip],'c'+i);ids.push(tip)}
 assert.equal(s.isAncestor(ids[0],ids[49]),true);assert.equal(s.isAncestor(ids[48],ids[49]),true);assert.equal(s.isAncestor(ids[49],ids[0]),false);const divergent=s.commit(files,[ids[10]],'side');assert.equal(s.isAncestor(divergent,ids[49]),false);assert.equal(s.isAncestor(ids[10],divergent),true);
});
test('upload-pack response cache invalidates when a ref moves',()=>{
 const s=new GitStore(),files=new Map();const root=s.commit(files,[],'root'),next=s.commit(files,[root],'next');s.setRef('refs/heads/main',root);
 const ls=Buffer.concat([packet('command=ls-refs\n'),packet('ref-prefix refs/heads/\n'),FLUSH]);
 const first=gitResponse(s,'git-upload-pack',false,true,ls).toString();assert.ok(first.includes(root));
 const again=gitResponse(s,'git-upload-pack',false,true,ls);assert.ok(again.includes(Buffer.from(root)));
 s.setRef('refs/heads/main',next);const moved=gitResponse(s,'git-upload-pack',false,true,ls).toString();assert.ok(moved.includes(next));assert.ok(!moved.includes(root+' refs/heads/main'));
});
test('pack encoding is cached and byte-stable across calls',()=>{
 const s=new GitStore();s.put('blob',Buffer.from('alpha'));s.put('blob',Buffer.from([0,1,2,255]));const objs=[...s.objects.values()];const a=pack(objs),b=pack(objs);assert.deepEqual(a,b);assert.equal(objectId('blob',Buffer.from('alpha')),objectId('blob',Buffer.from('alpha')));
});
test('raw transport serves keep-alive, pipelined and chunked requests',async()=>{
 process.env.MAKEFUN_SHOP_TOKEN='test-token';const server=createApp();await new Promise<void>(r=>server.listen(0,'127.0.0.1',r));const port=(server.address() as any).port;
 const dial=()=>new Promise<net.Socket>(res=>{const c=net.connect(port,'127.0.0.1',()=>res(c))});
 const readResponses=(sock:net.Socket,count:number)=>new Promise<Buffer[]>(res=>{const out:Buffer[]=[];let buf=Buffer.alloc(0);const on=(d:Buffer)=>{buf=Buffer.concat([buf,d]);for(;;){const i=buf.indexOf('\r\n\r\n');if(i<0)break;const m=/content-length: (\d+)/i.exec(buf.subarray(0,i).toString());if(!m)break;const end=i+4+parseInt(m[1],10);if(buf.length<end)break;out.push(buf.subarray(0,end));buf=buf.subarray(end);if(out.length>=count){sock.off('data',on);res(out);return}}};sock.on('data',on)});
 try{
  const sock=await dial();sock.setNoDelay(true);
  const get=Buffer.from('GET /api/v3/meta HTTP/1.1\r\nhost: x\r\nauthorization: Bearer test-token\r\nconnection: keep-alive\r\n\r\n');
  const p1=readResponses(sock,2);sock.write(Buffer.concat([get,get]));const two=await p1;assert.equal(two.length,2);for(const r of two)assert.ok(r.toString().includes('installed_version'));
  const chunked=Buffer.from('POST /mcp HTTP/1.1\r\nhost: x\r\nauthorization: Bearer test-token\r\ntransfer-encoding: chunked\r\nconnection: close\r\n\r\n');const body=Buffer.from(JSON.stringify({jsonrpc:'2.0',id:1,method:'ping'}));const framed=Buffer.concat([Buffer.from(body.length.toString(16)+'\r\n'),body,Buffer.from('\r\n0\r\n\r\n')]);
  const p2=readResponses(sock,1);sock.write(Buffer.concat([chunked,framed]));const cr=await p2;assert.ok(cr[0].toString().includes('"result"'));
  sock.destroy();
 }finally{server.closeAllConnections();await new Promise<void>(r=>server.close(()=>r()))}
});

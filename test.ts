import {test} from 'node:test';
import assert from 'node:assert/strict';
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

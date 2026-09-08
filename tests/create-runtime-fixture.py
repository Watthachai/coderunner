# Disposable integration fixture for the release checker; never a customer application.
from pathlib import Path
import tempfile,json,shutil
root=Path(__file__).resolve().parents[1]
p=Path(tempfile.mkdtemp(prefix='crn-production-fixture-'))
files={
'package.json': json.dumps({'name':'crn-runtime-fixture','version':'1.0.0','private':True,'type':'module','scripts':{'build':'next build --webpack','start':'next start','typecheck':'tsc --noEmit','test:unit':'vitest run tests/unit.test.ts','test:smoke':'playwright test','db:generate':'prisma generate','db:deploy':'prisma migrate deploy','db:seed':'node prisma/seed.mjs'},'dependencies':{'next':'16.2.9','react':'19.2.7','react-dom':'19.2.7','prisma':'6.19.0','@prisma/client':'6.19.0'},'devDependencies':{'typescript':'5.9.3','@types/node':'22.19.0','@types/react':'19.2.17','@types/react-dom':'19.2.3','vitest':'3.2.4','@playwright/test':'1.55.1'}},indent=2),
'next.config.mjs': 'export default {output:"standalone"};\n',
'crn-delivery.json': '{"schema_version":1,"database":true}',
'BUILD_NOTES.md':'Isolated runtime verification fixture only. No customer data or integrations.',
'TEST_CASES.md':'Assert browser persistence and initial administrator preservation.',
'PORT_CHECKLIST.md':'- [x] Item create/read/delete and reload persistence\n- [x] Initial administrator preservation',
'prisma/schema.prisma': '''generator client {
 provider = "prisma-client-js"
}
datasource db {
 provider = "postgresql"
 url = env("DATABASE_URL")
}
model User {
 id String @id @default(cuid())
 email String @unique
 passwordHash String
 mustChangePassword Boolean @default(true)
}
model SystemBootstrap {
 id String @id
 completedAt DateTime
}
model Item {
 id String @id @default(cuid())
 name String
}
''',
'prisma/migrations/migration_lock.toml':'provider = "postgresql"\n',
'prisma/migrations/202609070001_init/migration.sql':'''CREATE TABLE "User" ("id" TEXT PRIMARY KEY, "email" TEXT NOT NULL UNIQUE, "passwordHash" TEXT NOT NULL, "mustChangePassword" BOOLEAN NOT NULL DEFAULT true);
CREATE TABLE "SystemBootstrap" ("id" TEXT PRIMARY KEY, "completedAt" TIMESTAMP(3) NOT NULL);
CREATE TABLE "Item" ("id" TEXT PRIMARY KEY, "name" TEXT NOT NULL);
''',
'prisma/seed.mjs': '''import {PrismaClient} from '@prisma/client';
import {scryptSync,randomBytes} from 'node:crypto';
import {bootstrapOnce} from './bootstrap-once.mjs';
const prisma=new PrismaClient();
try { await bootstrapOnce(prisma,{
 hasUsers:async tx=>(await tx.user.count())>0,
 createAdmin:async(tx,{email,password})=>{const salt=randomBytes(16).toString('hex');await tx.user.create({data:{email,passwordHash:salt+':'+scryptSync(password,salt,32).toString('hex')}});}
}); } finally {await prisma.$disconnect();}
''',
'app/layout.tsx': '''import type {ReactNode} from 'react';
export default function Layout({children}:{children:ReactNode}){return <html lang="en"><body>{children}</body></html>;}''',
'app/page.tsx': ''''use client';
import {useEffect,useState} from 'react';
export default function Page(){const [items,setItems]=useState<{id:string,name:string}[]>([]); const [name,setName]=useState('');
 async function reload(){setItems(await(await fetch('/api/items')).json());}
 useEffect(()=>{void reload();},[]);
 return <main><h1>Runtime verification</h1><input aria-label="Item name" value={name} onChange={e=>setName(e.target.value)}/><button onClick={async()=>{await fetch('/api/items',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({name})});await reload();}}>Create</button><ul>{items.map(x=><li key={x.id}>{x.name}<button onClick={async()=>{await fetch('/api/items?id='+x.id,{method:'DELETE'});await reload();}}>Delete</button></li>)}</ul></main>;}
''',
'app/api/items/route.ts': '''import {PrismaClient} from '@prisma/client';
const db=new PrismaClient();
export const dynamic='force-dynamic';
export async function GET(){return Response.json(await db.item.findMany());}
export async function POST(r:Request){const body=await r.json();if(typeof body.name!=='string'||!body.name.trim())return Response.json({error:'Name required'},{status:400});return Response.json(await db.item.create({data:{name:body.name}}),{status:201});}
export async function DELETE(r:Request){await db.item.delete({where:{id:new URL(r.url).searchParams.get('id')??''}});return new Response(null,{status:204});}
''',
'lib/amount.ts':'export function amount(qty:number,price:number){if(qty<0||price<0)throw Error("negative");return qty*price;}\n',
 'tests/unit.test.ts':'''import {test,expect} from 'vitest';import {amount} from '../lib/amount';
test('amount calculation',()=>expect(amount(3,20)).toBe(60));
test('invalid quantity',()=>expect(()=>amount(-1,20)).toThrow());''',
 'playwright.config.ts':'''import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'./tests/browser',use:{baseURL:process.env.CRN_BASE_URL},workers:1,retries:0});''',
 'tests/browser/delivery.spec.ts':'''import {test,expect} from '@playwright/test';
import {PrismaClient} from '@prisma/client';
import {scryptSync} from 'node:crypto';
test('first account survives changed and absent bootstrap inputs',async()=>{const db=new PrismaClient();try{const users=await db.user.findMany();expect(users).toHaveLength(1);expect(users[0].email).toBe(process.env.CRN_TEST_ADMIN_EMAIL);const [salt,hash]=users[0].passwordHash.split(':');expect(scryptSync(process.env.CRN_TEST_ADMIN_PASSWORD!,salt,32).toString('hex')).toBe(hash);expect(users[0].mustChangePassword).toBe(true);expect(await db.systemBootstrap.count()).toBe(1);}finally{await db.$disconnect();}});
test('customer image persists input across reload and deletes committed records',async({page,request})=>{await page.goto('/');await expect(page.getByRole('heading')).toHaveText('Runtime verification');expect((await request.post('/api/items',{data:{name:''}})).status()).toBe(400);await page.getByLabel('Item name').fill('Customer record');await page.getByRole('button',{name:'Create'}).click();await expect(page.getByRole('listitem')).toContainText('Customer record');await page.reload();await expect(page.getByRole('listitem')).toContainText('Customer record');await page.getByRole('button',{name:'Delete'}).click();await expect(page.getByRole('listitem')).toHaveCount(0);});''',
 'tsconfig.json': json.dumps({'compilerOptions':{'target':'ES2017','lib':['dom','dom.iterable','esnext'],'allowJs':True,'skipLibCheck':True,'strict':True,'noEmit':True,'esModuleInterop':True,'module':'esnext','moduleResolution':'bundler','resolveJsonModule':True,'isolatedModules':True,'jsx':'preserve','plugins':[{'name':'next'}]},'include':['next-env.d.ts','**/*.ts','**/*.tsx','.next/types/**/*.ts'],'exclude':['node_modules']})
}
for f,s in files.items():
 q=p/f;q.parent.mkdir(parents=True,exist_ok=True);q.write_text(s)
shutil.copy(root/'cmd/server/skillassets/assets/bootstrap-once.mjs',p/'prisma/bootstrap-once.mjs')
print(p)

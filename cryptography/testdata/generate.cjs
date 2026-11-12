// Offline vector producer using the retained JS dependencies, never an installer.
const fs = require('node:fs');
const path = require('node:path');
const { BaseTrie: Trie } = require('../../../toolbox/node_modules/merkle-patricia-tree');
const rlp = require('../../../toolbox/node_modules/rlp');
const trieVersion = require('../../../toolbox/node_modules/merkle-patricia-tree/package.json').version;
const rlpVersion = require('../../../toolbox/node_modules/rlp/package.json').version;
if (trieVersion !== '4.2.4' || rlpVersion !== '3.0.0') throw Error('review vector dependency version change');
const bytes = value => Buffer.from(value);
const cases = [
  {name:'empty', pairs:[], key:bytes('missing')},
  {name:'single', pairs:[[bytes('a'),bytes('b')]], key:bytes('a')},
  {name:'absence', pairs:[[bytes('a'),bytes('b')]], key:bytes('d')},
  {name:'shared-prefix', pairs:[['do','verb'],['dog','puppy'],['doge','coins'],['horse','stallion']].map(p=>p.map(bytes)), key:bytes('dog')},
  {name:'binary', pairs:[[Buffer.from([0,255,1]),Buffer.from([255,0,128])],[Buffer.from([0,255,2]),Buffer.from([0,1])]], key:Buffer.from([0,255,1])},
  {name:'overwrite', pairs:[['a','old'],['b','other'],['a','new']].map(p=>p.map(bytes)), key:bytes('a')},
  {name:'delete', pairs:[['a','old'],['b','other'],['a','']].map(p=>p.map(bytes)), key:bytes('a')},
  {name:'empty-key', pairs:[[bytes(''),bytes('root-value')],[bytes('a'),bytes('b')]], key:bytes('')},
];
(async()=>{
  const mpt=[];
  for (const c of cases) {
    const trie=new Trie();
    await trie.batch(c.pairs.map(([key,value])=>({type:'put',key,value})));
    const proof=await Trie.createProof(trie,c.key);
    const value=await trie.get(c.key);
    if(c.pairs.length) {
      const checked=await Trie.verifyProof(trie.root,c.key,proof);
      if((checked===null)!==(value===null)||checked && !checked.equals(value)) throw Error('proof mismatch');
    }
    mpt.push({name:c.name,pairs:c.pairs.map(p=>p.map(b=>b.toString('hex'))),key:c.key.toString('hex'),value:value===null?null:value.toString('hex'),root:trie.root.toString('hex'),proof:proof.map(b=>b.toString('hex'))});
  }
  const rlpInputs={emptyString:'',cat:'cat',list:['cat','dog'],zero:0,integer1024:1024,nested:[[],'asdf',[[]],[[],[[]]]],binary:Buffer.from([0,255,128])};
  const rlpVectors=Object.entries(rlpInputs).map(([name,value])=>({name,encoded:Buffer.from(rlp.encode(value)).toString('hex')}));
  fs.writeFileSync(path.join(__dirname,'vectors.json'),JSON.stringify({source:{merklePatriciaTree:trieVersion,rlp:rlpVersion,node:process.version},mpt,rlp:rlpVectors},null,2)+'\n');
})();

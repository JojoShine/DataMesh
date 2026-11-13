'use strict';
const readline = require('node:readline');
const vm = require('node:vm');
const Decimal = require('decimal.js');
Decimal.set({precision:76,rounding:Decimal.ROUND_HALF_EVEN});
const write = process.stdout.write.bind(process.stdout);
console.log = console.info = console.warn = console.error = () => {};
const send = value => write(JSON.stringify(value) + '\n');
const failure = error => {
  const match = /<user_code>:(\d+)(?::(\d+))?/.exec(String(error?.stack ?? ''));
  const name = error instanceof TypeError ? 'TYPEERROR' : error instanceof SyntaxError ? 'SYNTAXERROR' : error instanceof RangeError ? 'RANGEERROR' : 'ERROR';
  return {code:'SCRIPT_'+name,line:match?Math.max(1,Number(match[1])):0,column:match?Number(match[2]??0):0};
};
const freeze = value => {if(value && typeof value==='object'){for(const child of Object.values(value))freeze(child);Object.freeze(value)}return value};
let transform, context;
(async()=>{
  for await(const line of readline.createInterface({input:process.stdin,crlfDelay:Infinity})){
    let message;
    try {
      message = JSON.parse(line);
      if(!transform){
        transform = vm.compileFunction(message.source+'\n;return typeof transform === "function" ? transform : null;',['Decimal','require'],{filename:'<user_code>'})(Decimal,require);
        if(typeof transform!=='function')throw new TypeError('Missing transform');
        context=freeze(message.context??{});
        send({type:'ready',sequence:0});
      }else{
        const rows=[];
        for(const row of message.rows){
          const values=await transform({...row.values},context);
          if(values===null||typeof values!=='object'||Array.isArray(values))throw new TypeError('Expected one object per input');
          for (const value of Object.values(values)) {
            if (value!==null && !["string","boolean","number"].includes(typeof value) || typeof value==="number" && !Number.isFinite(value)) throw new TypeError("Output values must be finite scalars");
          }
          rows.push({ordinal:row.ordinal,values});
        }
        send({type:'batch',sequence:message.sequence,rows});
      }
    }catch(error){send({type:transform?'batch':'ready',sequence:message?.sequence??0,error:failure(error)});process.exitCode=1;break}
  }
})().catch(()=>process.exit(1));

const prefix = process.env.GREETING_PREFIX || '你好';
const name = process.argv[2] || '朋友';
console.log(JSON.stringify({ message: `${prefix}，${name}` }));

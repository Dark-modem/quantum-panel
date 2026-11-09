const fs = require('fs');
let s = fs.readFileSync('ui11_smoke.py', 'utf8');
const q = String.fromCharCode(92) + 'S' + String.fromCharCode(43) + String.fromCharCode(41);
const o1 = 're.search(r"--name (' + q + ', cmds[c])';
const n1 = 're.search(r"--name (' + q + ', c)';
const o2 = 're.search(r"--token (' + q + ', cmds[c])';
const n2 = 're.search(r"--token (' + q + ', c)';
if (!s.includes(o1) || !s.includes(o2)) { console.error('markers missing'); process.exit(1); }
s = s.split(o1).join(n1);
s = s.split(o2).join(n2);
fs.writeFileSync('ui11_smoke.py', s);
console.log('fixed:', s.includes('cmds[c]') ? 'STILL BAD' : 'clean');

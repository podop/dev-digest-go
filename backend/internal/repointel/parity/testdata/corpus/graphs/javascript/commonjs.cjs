const a = require('./cjs_require_literal.js');
const b = require(`./cjs_require_template.js`);
const c = require(name);
const d = require('./cjs_' + 'concat.js');
const e = require(`./cjs_${'x'}.js`);
function late() {
  return require('./cjs_require_in_function.js');
}
const f = require('./cjs_require_call_chain.js').thing();
const { g } = require('./cjs_target.cjs');
module.exports = { a, b, c, d, e, f, g, late };

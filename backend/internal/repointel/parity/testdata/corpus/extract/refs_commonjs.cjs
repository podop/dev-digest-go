const { a } = require('./a');
const fs = require('fs');

function main() {
  a();
  fs.readFileSync('x');
  module.exports.exported();
}

module.exports = { main };
main();

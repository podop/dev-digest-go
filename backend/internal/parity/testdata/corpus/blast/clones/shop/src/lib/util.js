const { createInvoice } = require('../billing/invoice');

function helper() {
  return createInvoice('a', []);
}

module.exports = { helper };

import { computeTotal, formatInvoice } from '../billing/invoice';

function nightly() {
  const t = computeTotal([]);
  // formatInvoice( in a comment does not count
  return formatInvoice({ customer: 'n', items: [], total: t });
}

const weekly = () => computeTotal([{ price: 1 }]);

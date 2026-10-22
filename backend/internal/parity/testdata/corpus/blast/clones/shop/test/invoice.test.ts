import { createInvoice } from '../src/billing/invoice';

describe('invoice', () => {
  it('creates', () => {
    expect(createInvoice('a', [])).toBeDefined();
  });
});

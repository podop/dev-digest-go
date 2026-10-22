import { computeTotal } from './billing/invoice.js';

export function legacy() {
  return computeTotal([]);
}

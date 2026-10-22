// Invoice helpers. createInvoice( is only mentioned in this comment.
import { Money } from './money';

/**
 * createInvoice builds an invoice.
 */
export function createInvoice(customer: string, items: Item[]): Invoice {
  const label = "createInvoice(" + customer + ")";
  const total = computeTotal(items);
  return { customer, items, total };
}

export function computeTotal(items: Item[]): Money {
  return items.reduce((s, i) => s + i.price, 0);
}

export class InvoiceService {
  private readonly items: Item[] = [];

  total(): Money {
    return computeTotal(this.items);
  }

  static from(items: Item[]): InvoiceService {
    const s = new InvoiceService();
    return s;
  }
}

export const formatInvoice = (inv: Invoice): string => `Invoice for ${inv.customer}`;

export interface Invoice {
  customer: string;
  items: Item[];
  total: Money;
}

export type Item = { price: number };

export enum Status {
  Draft,
  Sent,
}

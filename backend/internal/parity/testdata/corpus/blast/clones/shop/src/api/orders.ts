import { createInvoice, InvoiceService } from '../billing/invoice';

export async function placeOrder(req: Request) {
  const invoice = createInvoice(req.customer, req.items);
  const svc = new InvoiceService();
  return svc.total() + invoice.total;
}

export const listOrders = async () => {
  return [createInvoice('x', [])];
};

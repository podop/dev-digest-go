import { Status, formatInvoice } from '../billing/invoice';

export function OrderView(props: { inv: Invoice }) {
  const text = formatInvoice(props.inv);
  return <InvoiceBadge status={Status.Sent} text={text} />;
}

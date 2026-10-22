import { Widget } from './widget';

export function view() {
  return <Widget label={format()} />;
}

export const other = () => <div><Child /></div>;

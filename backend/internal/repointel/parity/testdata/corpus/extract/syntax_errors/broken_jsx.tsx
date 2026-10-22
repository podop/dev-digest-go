export function Ok() {
  return <div />;
}

export function Broken() {
  return <div><span></div>;
}

export function Tail() {
  return <Ok />;
}

// In a .ts file angle brackets are type assertions, not JSX.
function cast(x: unknown) {
  const a = <Foo>x;
  const b = <Foo.Bar>x;
  return convert(a, b);
}
const generic = identity<string>('x');
const newGeneric = new Box<number>(1);

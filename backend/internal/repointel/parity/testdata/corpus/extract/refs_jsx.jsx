import { Button } from './button';

export function Page() {
  return (
    <section>
      <Button onClick={() => go()} />
      <Panel.Header />
      <footer />
      <Page />
    </section>
  );
}

function Shell() {
  return <Page extra={compute()} />;
}

import { Imported } from './imported';
import React from 'react';

export function App() {
  return (
    <div className="root">
      <Imported prop={1} />
      <Local.Nested>
        <span>text</span>
      </Local.Nested>
      <ns.Widget />
      <lower.Case />
      <Self />
      <Generic<string> value="x" />
      <></>
      <Frag.Ment />
      <Outer>
        <Inner />
      </Outer>
      {cond && <Cond />}
      {list.map((i) => <Item key={i} />)}
      {render()}
      <Attr render={<InAttr />} onClick={handler()} />
    </div>
  );
}

function Self() {
  return <Self2 />;
}

const Arrow = () => <Arrow />;
const Wrapper = () => (
  <Provider value={make()}>
    <Child />
  </Provider>
);
class Comp extends React.Component {
  render() {
    return <Rendered />;
  }
}
const lowercaseVar = () => <_private />;
const dollar = () => <$Dollar />;

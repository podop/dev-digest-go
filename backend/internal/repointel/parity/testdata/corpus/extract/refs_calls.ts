import { imported, other } from './mod';
import def, * as ns from './ns';

const alpha = () => 1;

function declaredHere() {
  alpha();
  imported();
  obj.method();
  this.call();
  a.b.c.deep();
  a.b().chained();
  callbacks[0]();
  (function iife() {})();
  (() => 1)();
  fn?.();
  obj?.maybe();
  getFactory()();
  new Plain();
  new ns.Namespaced();
  new a.b.Deep();
  new (getCtor())();
  new Generic<string>();
  super.parent();
  import('./dyn');
  require('./req');
  [1, 2].map(transform);
  if (check(1)) { nested(); }
  for (const x of listOf()) { use(x); }
  return later(1) + later(2);
}

class K {
  run() {
    helper();
    this.run();
  }
}

const viaTemplate = `${interpolated()} text`;
alpha(); alpha();
twice(); twice();
sameLine(); sameLine(); other2();
declaredHere();
function k() { k(); }
const self = () => self();
if (typeof guard === 'function') guard();
delete obj.prop; void voided(); await awaited(); yield_(1);

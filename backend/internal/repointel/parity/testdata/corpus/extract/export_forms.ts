function alpha() {}
function beta() {}
const gamma = () => 1;
class Delta {
  m() {}
}
interface Eps {}
type Zeta = number;
enum Eta {
  A,
}

export { alpha, beta as renamedBeta };
export { gamma, Delta };
export { Eps, Zeta, Eta };
export { alpha as again } from './elsewhere';
export * from './star';
export * as ns from './ns';
export default function defaultFn() {}

import { a, b as renamed } from './mod';
import type { T } from './types';
import def from './def';
import * as everything from './all';
import './side-effect';
export { x } from './x';
export * from './y';

const value = require('./cjs-in-ts');
function declared() { useIt(); }
declared();

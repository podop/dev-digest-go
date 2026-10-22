// case: value_used
import { a1 } from './value_used';
export const r1 = a1();

// case: unused_named
import { a2 } from './unused_named';

// case: unused_default
import d3 from './unused_default';

// case: unused_namespace
import * as n4 from './unused_namespace';

// case: type_annotation_only
import { T5 } from './type_annotation_only';
export const r5: T5 = null as unknown as T5;

// case: type_only_kw
import type { T6 } from './type_only_kw';
export type R6 = T6;

// case: type_only_default_kw
import type D7 from './type_only_default_kw';
export type R7 = D7;

// case: inline_type_all
import { type A8, type B8 } from './inline_type_all';
export type R8 = [A8, B8];

// case: inline_type_mixed
import { type A9, b9 } from './inline_type_mixed';
export const r9 = b9();

// case: inline_type_mixed_unused_value
import { type A10, b10 } from './inline_type_mixed_unused_value';
export type R10 = A10;

// case: side_effect
import './side_effect';

// case: empty_braces
import {} from './empty_braces';

// case: import_equals
import eq11 = require('./import_equals');
export const r11 = eq11;

// case: export_type_from
export type { X12 } from './export_type_from';

// case: export_from_value
export { y13 } from './export_from_value';

// case: export_from_inline_type
export { type Z14 } from './export_from_inline_type';

// case: export_from_mixed
export { type Z15, w15 } from './export_from_mixed';

// case: export_star
export * from './export_star';

// case: export_star_as
export * as ns16 from './export_star_as';

// case: export_star_type
export type * from './export_star_type';

// case: reexport_local
import { re17 } from './reexport_local';
export { re17 };

// case: reexport_local_renamed
import { re18 } from './reexport_local_renamed';
export { re18 as renamed18 };

// case: export_default_name
import dn19 from './export_default_name';
export default dn19;

// case: decorator
import { dec20 } from './decorator';
@dec20
export class Decorated20 {}

// case: typeof_type
import { tq21 } from './typeof_type';
export type TT21 = typeof tq21;

// case: typeof_value
import { tv22 } from './typeof_value';
export const k22 = typeof tv22;

// case: generic_arg
import { G23 } from './generic_arg';
export const arr23: Array<G23> = [];

// case: implements_only
import { Iface24 } from './implements_only';
export class Impl24 implements Iface24 {}

// case: extends_value
import { Base25 } from './extends_value';
export class D25 extends Base25 {}

// case: interface_extends
import { IBase26 } from './interface_extends';
export interface J26 extends IBase26 {}

// case: shadowed
import { sh27 } from './shadowed';
export function f27(sh27: number) { return sh27; }

// case: used_in_comment
import { cm28 } from './used_in_comment';
// cm28();

// case: used_in_string
import { st29 } from './used_in_string';
export const s29 = 'st29';

// case: default_named
import dflt30, { n30 } from './default_named';
export const q30 = n30;

// case: default_named_default_used
import dflt31, { n31 } from './default_named_default_used';
export const q31 = dflt31;

// case: namespace_type
import * as nt32 from './namespace_type';
export let z32: nt32.T;

// case: namespace_value
import * as nv33 from './namespace_value';
export const w33 = nv33.a;

// case: type_and_value
import { tv34 } from './type_and_value';
export type X34 = typeof tv34;
export const y34 = tv34();

// case: declare_typeof
import { dc35 } from './declare_typeof';
declare const x35: typeof dc35;

// case: export_type_name
import { Tn36 } from './export_type_name';
export { Tn36 };

// case: class_property_type
import { P37 } from './class_property_type';
export class C37 { p!: P37; }

// case: class_property_init
import { P38 } from './class_property_init';
export class C38 { p = new P38(); }

// case: as_cast_type
import { C39 } from './as_cast_type';
export const x39 = {} as C39;

// case: satisfies_type
import { S40 } from './satisfies_type';
export const x40 = {} satisfies S40;

// case: new_expr
import { K41 } from './new_expr';
export const x41 = new K41();

// case: arrow_param_shadow
import { s42 } from './arrow_param_shadow';
export const f42 = (s42: number) => s42;

// case: enum_member
import { e43 } from './enum_member';
export enum E43 { A = 1 }
export const z43 = E43.A;

// case: computed_key
import { k44 } from './computed_key';
export const o44 = { [k44]: 1 };

// case: template
import { t45 } from './template';
export const s45 = `${t45}`;

// case: object_shorthand
import { sh46 } from './object_shorthand';
export const o46 = { sh46 };

// case: jsdoc_only
import { j47 } from './jsdoc_only';
/** @type {j47} */
export const x47 = 1;

// case: dynamic_literal
export const d48 = import('./dynamic_literal');

// case: dynamic_template
export const d49 = import(`./dynamic_template`);

// case: dynamic_nonliteral
declare const name50: string;
export const d50 = import(name50);

// case: dynamic_concat
export const d51 = import('./dynamic_' + 'concat');

// case: dynamic_template_expr
declare const v52: string;
export const d52 = import(`./dynamic_${v52}`);

// case: require_literal
declare const require: (s: string) => unknown;
export const r53 = require('./require_literal');

// case: require_nonliteral
declare const require: (s: string) => unknown;
declare const n54: string;
export const r54 = require(n54);


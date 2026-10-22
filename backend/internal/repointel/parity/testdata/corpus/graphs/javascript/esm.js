import { unused1 } from './es_named_unused.js';
import unused2 from './es_default_unused.js';
import * as unused3 from './es_namespace_unused.js';
import './es_side_effect.js';
import dflt, { named } from './es_default_and_named.js';
export { x1 } from './es_export_from.js';
export { y1 as y2 } from './es_reexport_named_alias.js';
export * from './es_export_star.js';
export * as starred from './es_export_star_as.js';
import data from './es_json_attr_target.js' with { type: 'json' };
const lazy = () => import('./es_dynamic_literal.js');
const lazy2 = () => import(`./es_dynamic_template.js`);
const lazy3 = (n) => import(n);
const lazy4 = (n) => import(`./dyn_${n}.js`);
const lazy5 = () => import('./es_dynamic_with_options.js', { with: { type: 'json' } });
// import './in_comment.js';
const s = "import './in_string.js'";
/** @typedef {import('./es_type_comment_unused.js').T} T */

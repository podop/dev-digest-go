import { Used } from './jsx_used';
import { Unused } from './jsx_unused';
import { Member } from './jsx_member';
import { lower } from './jsx_lower';
import { Fragment } from './jsx_fragment';
import { NotJsx } from './jsx_not_jsx';
import { Attr } from './jsx_attr';

export const a = <Used />;
export const b = <div />;
export const c = <Member.Part />;
export const d = <lower />;
export const e = <></>;
export const f = <div title={Attr} />;

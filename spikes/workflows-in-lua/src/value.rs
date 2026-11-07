//! Everything that crosses between Rust and a script does so as a `Node`: plain data, plus the
//! opaque handles from `taint`. It is what lets facts leave one VM and enter another.
use crate::taint::{CheckedPatch, Tainted, Ticket};
use almanac::prelude::*;
use mlua::{Function, Lua, Table, Value as LuaValue};
use serde_json::{Map, Value, json};
use std::collections::BTreeMap;

const MAX_DEPTH: usize = 32;
const STRICT: &str = "almanac.strict";

#[derive(Clone)]
pub enum Node {
    Nil,
    Bool(bool),
    Num(f64),
    Str(String),
    List(Vec<Node>),
    Map(BTreeMap<String, Node>),
    Tainted(Tainted),
    Proof(usize),
    Checked(Checked),
}

impl Node {
    pub fn map<const N: usize>(entries: [(&str, Node); N]) -> Self {
        Self::Map(
            entries
                .map(|(k, v)| (k.to_string(), v))
                .into_iter()
                .collect(),
        )
    }

    pub fn text(value: impl ToString) -> Self {
        Self::Str(value.to_string())
    }

    /// Reads a Lua value, keeping only data and the host's own handles. Functions, threads, and
    /// anything else that could carry behaviour or state across a boundary are refused.
    pub fn from_lua(value: &LuaValue) -> Result<Self, String> {
        Self::read(value, 0)
    }

    fn read(value: &LuaValue, depth: usize) -> Result<Self, String> {
        if depth > MAX_DEPTH {
            return Err("a value is nested too deeply, or refers to itself".to_string());
        }
        match value {
            LuaValue::Nil => Ok(Self::Nil),
            LuaValue::Boolean(b) => Ok(Self::Bool(*b)),
            LuaValue::Integer(n) => Ok(Self::Num(*n as f64)),
            LuaValue::Number(n) if n.is_finite() => Ok(Self::Num(*n)),
            LuaValue::Number(_) => Err("a number is not finite".to_string()),
            LuaValue::String(s) => s
                .to_str()
                .map(|s| Self::Str(s.to_string()))
                .map_err(|_| "a string is not UTF-8".to_string()),
            LuaValue::Table(t) => Self::read_table(t, depth),
            LuaValue::UserData(ud) => {
                if let Ok(tainted) = ud.borrow::<Tainted>() {
                    return Ok(Self::Tainted(tainted.clone()));
                }
                if let Ok(ticket) = ud.borrow::<Ticket>() {
                    return Ok(Self::Proof(ticket.0));
                }
                if let Ok(checked) = ud.borrow::<CheckedPatch>() {
                    return Ok(Self::Checked(checked.0.clone()));
                }
                Err("a value is userdata the host did not make".to_string())
            }
            other => Err(format!(
                "a {} cannot cross into the host: only data can",
                other.type_name()
            )),
        }
    }

    fn read_table(table: &Table, depth: usize) -> Result<Self, String> {
        let length = table.raw_len();
        let mut list = vec![Self::Nil; length];
        let mut map = BTreeMap::new();
        for pair in table.pairs::<LuaValue, LuaValue>() {
            let (key, value) = pair.map_err(|e| e.to_string())?;
            let value = Self::read(&value, depth + 1)?;
            match key {
                LuaValue::Integer(i) if i >= 1 && i as usize <= length => {
                    list[i as usize - 1] = value
                }
                LuaValue::Number(i) if i >= 1.0 && i.fract() == 0.0 && i as usize <= length => {
                    list[i as usize - 1] = value;
                }
                LuaValue::String(s) => {
                    let key = s.to_str().map_err(|_| "a key is not UTF-8".to_string())?;
                    map.insert(key.to_string(), value);
                }
                _ => return Err("a table key must be a string or a list index".to_string()),
            }
        }
        match (length, map.is_empty()) {
            (0, _) => Ok(Self::Map(map)),
            (_, true) => Ok(Self::List(list)),
            _ => Err("a table is both a list and a map".to_string()),
        }
    }

    /// A value a script reads. Host tables are frozen, and reading a field that does not exist is
    /// an error rather than `nil`, so a typo fails where it was made.
    pub fn to_lua(&self, lua: &Lua) -> mlua::Result<LuaValue> {
        Ok(match self {
            Self::Nil => LuaValue::Nil,
            Self::Bool(b) => LuaValue::Boolean(*b),
            Self::Num(n) => LuaValue::Number(*n),
            Self::Str(s) => LuaValue::String(lua.create_string(s)?),
            Self::List(items) => {
                let table = lua.create_table()?;
                for (i, item) in items.iter().enumerate() {
                    table.raw_seti(i + 1, item.to_lua(lua)?)?;
                }
                sealed(lua, table)?
            }
            Self::Map(entries) => {
                let table = lua.create_table()?;
                for (key, value) in entries {
                    table.raw_set(key.as_str(), value.to_lua(lua)?)?;
                }
                sealed(lua, table)?
            }
            Self::Tainted(t) => LuaValue::UserData(lua.create_userdata(t.clone())?),
            Self::Proof(i) => LuaValue::UserData(lua.create_userdata(Ticket(*i))?),
            Self::Checked(c) => LuaValue::UserData(lua.create_userdata(CheckedPatch(c.clone()))?),
        })
    }

    /// Reads an event as `serde` wrote it, turning `Untrusted`'s tag back into a handle.
    pub fn from_observed(value: &Value) -> Self {
        match value {
            Value::Null => Self::Nil,
            Value::Bool(b) => Self::Bool(*b),
            Value::Number(n) => Self::Num(n.as_f64().unwrap_or(f64::NAN)),
            Value::String(s) => Self::Str(s.clone()),
            Value::Array(items) => Self::List(items.iter().map(Self::from_observed).collect()),
            Value::Object(entries) => match entries.get("$untrusted") {
                Some(Value::String(text)) if entries.len() == 1 => {
                    Self::Tainted(Tainted::Text(Untrusted::new(text.clone())))
                }
                Some(other) if entries.len() == 1 => {
                    Self::Tainted(Tainted::Answer(Untrusted::new(other.clone())))
                }
                _ => Self::Map(
                    entries
                        .iter()
                        .map(|(k, v)| (k.clone(), Self::from_observed(v)))
                        .collect(),
                ),
            },
        }
    }

    /// What an agent receives, with tainted values still tagged. Proofs and checked patches mean
    /// nothing to an agent, so they are refused.
    pub fn to_agent_input(&self) -> Result<Value, String> {
        Ok(match self {
            Self::Nil => Value::Null,
            Self::Bool(b) => json!(b),
            Self::Num(n) => json!(n),
            Self::Str(s) => json!(s),
            Self::List(items) => Value::Array(
                items
                    .iter()
                    .map(Self::to_agent_input)
                    .collect::<Result<_, _>>()?,
            ),
            Self::Map(entries) => {
                let mut out = Map::new();
                for (key, value) in entries {
                    out.insert(key.clone(), value.to_agent_input()?);
                }
                Value::Object(out)
            }
            Self::Tainted(t) => t.tagged().map_err(|e| e.to_string())?,
            Self::Proof(_) | Self::Checked(_) => {
                return Err("a proof is not agent input".to_string());
            }
        })
    }

    /// Whether anything in here is a `Cooled` proof, which must not outlive `gather`.
    pub fn holds_proof(&self) -> bool {
        match self {
            Self::Proof(_) => true,
            Self::List(items) => items.iter().any(Self::holds_proof),
            Self::Map(entries) => entries.values().any(Self::holds_proof),
            _ => false,
        }
    }

    pub fn holds_taint(&self) -> bool {
        match self {
            Self::Tainted(_) => true,
            Self::List(items) => items.iter().any(Self::holds_taint),
            Self::Map(entries) => entries.values().any(Self::holds_taint),
            _ => false,
        }
    }

    pub fn field(&self, name: &str) -> &Node {
        match self {
            Self::Map(entries) => entries.get(name).unwrap_or(&Self::Nil),
            _ => &Self::Nil,
        }
    }

    pub fn as_str(&self) -> Option<&str> {
        match self {
            Self::Str(s) => Some(s),
            _ => None,
        }
    }

    /// A whole number in `u64`'s range. Lua numbers are doubles, so this is exact below 2^53.
    pub fn as_u64(&self) -> Option<u64> {
        match self {
            Self::Num(n) if *n >= 0.0 && n.fract() == 0.0 && *n < 9_007_199_254_740_992.0 => {
                Some(*n as u64)
            }
            _ => None,
        }
    }

    /// An empty table reads as a map, because Lua cannot tell, so this accepts both.
    pub fn as_list(&self) -> Option<&[Node]> {
        match self {
            Self::List(items) => Some(items),
            Self::Map(entries) if entries.is_empty() => Some(&[]),
            _ => None,
        }
    }

    /// The name of this value's shape, for an error. It never includes the value.
    pub fn shape(&self) -> &'static str {
        match self {
            Self::Nil => "nil",
            Self::Bool(_) => "a boolean",
            Self::Num(_) => "a number",
            Self::Str(_) => "a string",
            Self::List(_) => "a list",
            Self::Map(_) => "a table",
            Self::Tainted(_) => "Untrusted",
            Self::Proof(_) => "a Cooled proof",
            Self::Checked(_) => "a Checked patch",
        }
    }
}

fn sealed(lua: &Lua, table: Table) -> mlua::Result<LuaValue> {
    table.set_metatable(Some(lua.named_registry_value::<Table>(STRICT)?))?;
    table.set_readonly(true);
    Ok(LuaValue::Table(table))
}

/// Installs the metatable `to_lua` seals tables with.
pub fn install_strict(lua: &Lua) -> mlua::Result<()> {
    let missing: Function =
        lua.create_function(|_, (_, key): (LuaValue, LuaValue)| -> mlua::Result<()> {
            let key = key.to_string().unwrap_or_default();
            Err(mlua::Error::runtime(format!(
                "no field `{key}` here: check the name against what the host declares"
            )))
        })?;
    let strict = lua.create_table()?;
    strict.raw_set("__index", missing)?;
    strict.raw_set("__metatable", "sealed")?;
    strict.set_readonly(true);
    lua.set_named_registry_value(STRICT, strict)
}

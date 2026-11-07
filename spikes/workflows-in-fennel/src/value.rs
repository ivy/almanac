//! What crosses between the host and a script. The rule this module keeps: hostile text is never a
//! Lua string. It stays in `Tainted`, a userdata with no methods and no metamethods, so every Lua
//! string in a VM is clean by construction.
use almanac::prelude::*;
use almanac_renovate::update::{PullText, Update, update_of};
use mlua::{Lua, UserData, Value as LuaValue};
use serde::Serialize;
use serde::de::DeserializeOwned;
use serde_json::{Map, Value, json};
use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, Mutex};

/// A string a step or an effect holds for the life of the process. `Cx` and `Effect` take
/// `&'static str` for these because in Rust they are literals. Here they come from a script, so
/// the set is bounded by what its source can build, and not by a literal's being in the binary.
pub fn intern(text: &str) -> &'static str {
    static TABLE: Mutex<BTreeSet<&'static str>> = Mutex::new(BTreeSet::new());
    let mut table = TABLE.lock().unwrap();
    if let Some(found) = table.get(text) {
        return found;
    }
    let leaked: &'static str = Box::leak(text.to_string().into_boxed_str());
    table.insert(leaked);
    leaked
}

/// Hostile content, held by the host. Scripts can pass this around and nothing else.
#[derive(Clone)]
pub enum Tainted {
    Text(Untrusted<String>),
    Pull(Untrusted<PullText>),
    Update(Untrusted<Update>),
    Patch(Untrusted<Patch>),
    /// What a quarantined agent answered.
    Answer(Untrusted<Value>),
}

/// No methods and no metamethods, so indexing, concatenation, and arithmetic all fail in Lua, and
/// `tostring` can print a type name at most.
impl UserData for Tainted {}

/// Reads tainted content inside the host. Scripts never reach this: it serves providers, the
/// agent boundary, and narrowers, in the same way as `update::opened`.
pub fn reveal<T: Serialize + DeserializeOwned>(tainted: &Untrusted<T>) -> Result<T, StepError> {
    let tagged = serde_json::to_value(tainted)?;
    Ok(serde_json::from_value(tagged["$untrusted"].clone())?)
}

impl Tainted {
    fn kind(&self) -> &'static str {
        match self {
            Self::Text(_) => "text",
            Self::Pull(_) => "pull",
            Self::Update(_) => "update",
            Self::Patch(_) => "patch",
            Self::Answer(_) => "answer",
        }
    }

    /// The `{"$untrusted": ...}` form agents and stored records use.
    fn tagged(&self) -> Value {
        let tagged = match self {
            Self::Text(t) => serde_json::to_value(t),
            Self::Pull(t) => serde_json::to_value(t),
            Self::Update(t) => serde_json::to_value(t),
            Self::Patch(t) => serde_json::to_value(t),
            Self::Answer(t) => serde_json::to_value(t),
        };
        tagged.unwrap_or(Value::Null)
    }

    fn from_tagged(value: &Value) -> Result<Self, StepError> {
        let value = value.clone();
        let kind = value["$kind"].as_str().unwrap_or_default().to_string();
        Ok(match kind.as_str() {
            "pull" => Self::Pull(serde_json::from_value(value)?),
            "update" => Self::Update(serde_json::from_value(value)?),
            "patch" => Self::Patch(serde_json::from_value(value)?),
            "answer" => Self::Answer(serde_json::from_value(value)?),
            _ => Self::Text(serde_json::from_value(value)?),
        })
    }

    /// The content as plain Lua values, for a narrower's own VM and nowhere else.
    pub fn raw(&self) -> Result<Value, StepError> {
        Ok(self.tagged()["$untrusted"].clone())
    }
}

/// `Untrusted::map`, by name. A script cannot pass a function, because a function would see the
/// content, so the set of maps is closed and lives here.
pub fn map(tainted: &Tainted, mapper: &str) -> Result<Tainted, String> {
    match (mapper, tainted) {
        ("update-of", Tainted::Pull(text)) => Ok(Tainted::Update(text.map(update_of))),
        ("diff", Tainted::Patch(patch)) => Ok(Tainted::Text(patch.map(|p| p.diff.clone()))),
        _ => Err(format!(
            "no map `{mapper}` for {} content: maps are closed and defined by the host",
            tainted.kind()
        )),
    }
}

/// A value a script can hold that survives a VM. Every `Text` here is clean.
#[derive(Clone)]
pub enum Script {
    Nil,
    Bool(bool),
    Int(i64),
    Float(f64),
    Text(String),
    List(Vec<Script>),
    Map(BTreeMap<String, Script>),
    Tainted(Tainted),
}

impl Script {
    pub fn map<const N: usize>(entries: [(&str, Script); N]) -> Self {
        let pairs = entries.into_iter().map(|(k, v)| (k.to_string(), v));
        Self::Map(pairs.collect())
    }

    pub fn text(text: impl ToString) -> Self {
        Self::Text(text.to_string())
    }

    pub fn get(&self, key: &str) -> Option<&Script> {
        match self {
            Self::Map(map) => map.get(key),
            _ => None,
        }
    }

    fn json(&self, kinds: bool) -> Value {
        match self {
            Self::Nil => Value::Null,
            Self::Bool(b) => json!(b),
            Self::Int(i) => json!(i),
            Self::Float(f) => json!(f),
            Self::Text(t) => json!(t),
            Self::List(items) => Value::Array(items.iter().map(|i| i.json(kinds)).collect()),
            Self::Map(map) => {
                let pairs = map.iter().map(|(k, v)| (k.clone(), v.json(kinds)));
                Value::Object(pairs.collect::<Map<_, _>>())
            }
            Self::Tainted(tainted) => {
                let mut tagged = tainted.tagged();
                if kinds {
                    tagged["$kind"] = json!(tainted.kind());
                }
                tagged
            }
        }
    }

    /// The form stored in the run log. `$kind` lets a replay rebuild the same `Tainted`.
    pub fn to_record(&self) -> Value {
        self.json(true)
    }

    /// The form an agent receives, with taint markers intact.
    pub fn to_wire(&self) -> Value {
        self.json(false)
    }

    /// Rebuilds a value from the run log, or from an event the host parsed. Never call this on a
    /// provider's raw JSON: every string in it would arrive clean.
    pub fn from_record(value: &Value) -> Result<Self, StepError> {
        Ok(match value {
            Value::Null => Self::Nil,
            Value::Bool(b) => Self::Bool(*b),
            Value::Number(n) => match n.as_i64() {
                Some(i) => Self::Int(i),
                None => Self::Float(n.as_f64().unwrap_or_default()),
            },
            Value::String(s) => Self::Text(s.clone()),
            Value::Array(items) => {
                let items: Result<Vec<_>, _> = items.iter().map(Self::from_record).collect();
                Self::List(items?)
            }
            Value::Object(map) if map.contains_key("$untrusted") => {
                Self::Tainted(Tainted::from_tagged(value)?)
            }
            Value::Object(map) => {
                let mut out = BTreeMap::new();
                for (key, item) in map {
                    out.insert(key.clone(), Self::from_record(item)?);
                }
                Self::Map(out)
            }
        })
    }

    /// Everything but a proof, an effect, or a function can cross. Those stay in the VM that made
    /// them, which is what keeps them from being stored and replayed.
    pub fn from_lua(value: &LuaValue) -> Result<Self, String> {
        Self::read(value, 0)
    }

    fn read(value: &LuaValue, depth: usize) -> Result<Self, String> {
        if depth > 32 {
            return Err("a value nests too deeply to record".to_string());
        }
        Ok(match value {
            LuaValue::Nil => Self::Nil,
            LuaValue::Boolean(b) => Self::Bool(*b),
            LuaValue::Integer(i) => Self::Int(*i),
            LuaValue::Number(f) => Self::Float(*f),
            LuaValue::String(s) => {
                let text = s.to_str().map_err(|_| "a string is not UTF-8".to_string())?;
                Self::Text(text.to_string())
            }
            LuaValue::Table(table) => Self::read_table(table, depth)?,
            LuaValue::UserData(data) => match data.borrow::<Tainted>() {
                Ok(tainted) => Self::Tainted(tainted.clone()),
                Err(_) => return Err("a proof or an effect cannot leave its VM".to_string()),
            },
            other => {
                let kind = other.type_name();
                return Err(format!("a {kind} cannot be recorded"));
            }
        })
    }

    fn read_table(table: &mlua::Table, depth: usize) -> Result<Self, String> {
        let len = table.raw_len();
        let mut list = vec![Self::Nil; len];
        let mut map = BTreeMap::new();
        for pair in table.pairs::<LuaValue, LuaValue>() {
            let (key, value) = pair.map_err(|e| e.to_string())?;
            let value = Self::read(&value, depth + 1)?;
            match key {
                LuaValue::Integer(at) if at >= 1 && at as usize <= len => {
                    list[at as usize - 1] = value;
                }
                LuaValue::String(key) => {
                    let key = key.to_str().map_err(|_| "a key is not UTF-8".to_string())?;
                    map.insert(key.to_string(), value);
                }
                _ => return Err("a table key must be a string or a list index".to_string()),
            }
        }
        match (list.is_empty(), map.is_empty()) {
            (false, false) => Err("a table is both a list and a map".to_string()),
            (true, _) => Ok(Self::Map(map)),
            (false, true) => Ok(Self::List(list)),
        }
    }

    pub fn into_lua(&self, lua: &Lua) -> mlua::Result<LuaValue> {
        Ok(match self {
            Self::Nil => LuaValue::Nil,
            Self::Bool(b) => LuaValue::Boolean(*b),
            Self::Int(i) => LuaValue::Integer(*i),
            Self::Float(f) => LuaValue::Number(*f),
            Self::Text(t) => LuaValue::String(lua.create_string(t)?),
            Self::List(items) => {
                let table = lua.create_table()?;
                for (at, item) in items.iter().enumerate() {
                    table.raw_set(at + 1, item.into_lua(lua)?)?;
                }
                LuaValue::Table(table)
            }
            Self::Map(map) => {
                let table = lua.create_table()?;
                for (key, item) in map {
                    table.raw_set(key.as_str(), item.into_lua(lua)?)?;
                }
                LuaValue::Table(table)
            }
            Self::Tainted(tainted) => LuaValue::UserData(lua.create_userdata(tainted.clone())?),
        })
    }
}

/// Raw JSON as plain Lua. Only `narrow` uses this, in a VM that holds nothing else.
pub fn raw_into_lua(lua: &Lua, value: &Value) -> mlua::Result<LuaValue> {
    Ok(match value {
        Value::Null => LuaValue::Nil,
        Value::Bool(b) => LuaValue::Boolean(*b),
        Value::Number(n) => match n.as_i64() {
            Some(i) => LuaValue::Integer(i),
            None => LuaValue::Number(n.as_f64().unwrap_or_default()),
        },
        Value::String(s) => LuaValue::String(lua.create_string(s)?),
        Value::Array(items) => {
            let table = lua.create_table()?;
            for (at, item) in items.iter().enumerate() {
                table.raw_set(at + 1, raw_into_lua(lua, item)?)?;
            }
            LuaValue::Table(table)
        }
        Value::Object(map) => {
            let table = lua.create_table()?;
            for (key, item) in map {
                table.raw_set(key.as_str(), raw_into_lua(lua, item)?)?;
            }
            LuaValue::Table(table)
        }
    })
}

/// Proof that `cx.cooled` passed. The script holds a handle and the host holds the proof, so a
/// table cannot pose as one, and it cannot be recorded or outlive its VM.
pub struct Proof(pub Arc<Cooled>);
impl UserData for Proof {}

/// A publish-time read for `cx.cooled` to perform. Holding one proves nothing: the host does the
/// read, so a script cannot hand it a forged time.
pub struct AgeRead(pub Untrusted<Update>);
impl UserData for AgeRead {}

/// A patch that passed `DEFAULT_POLICY`. Only `patches.check` makes one.
pub struct CheckedPatch(pub Checked);
impl UserData for CheckedPatch {}

/// An effect. Only the constructors in `effect` make one, so the set stays closed.
pub struct Planned(pub Effect);
impl UserData for Planned {}

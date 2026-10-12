# damstack

[English](README.md) | [Русский](README.ru.md) | Español

Tu propio servidor, configurado y mantenido desde tu Mac con un solo programa.

Alquilas un servidor, respondes unas pocas preguntas y damstack lo convierte
en una plataforma privada: acceso solo con tu clave, alcanzable a través de tu
propia red WireGuard, con Nomad, Consul y Vault para ejecutar aplicaciones,
certificados de Let's Encrypt y copias de seguridad nocturnas que se copian a
tu Mac. Después le añades aplicaciones, como tu propio servidor de correo, con
un comando cada una.

No necesitas saber nada de servidores. damstack pregunta lo que necesita,
explica lo que hace y comprueba que el servidor sigue como lo configuraste.

## Qué necesitas

- Un Mac, Apple silicon o Intel, con [Homebrew](https://brew.sh).
- La [app de WireGuard](https://apps.apple.com/app/wireguard/id1451685025),
  para la red privada entre tus dispositivos y el servidor.
- Un servidor con Ubuntu 24.04, de cualquier proveedor: su dirección IP y la
  contraseña de root que te da el proveedor. Bastan 2 CPU, 4 GB de memoria y
  20 GB de disco.
- Un dominio cuyo DNS esté en [Cloudflare](https://www.cloudflare.com), y un
  token de API allí que pueda editar el DNS de ese dominio. Otros proveedores
  de DNS también funcionan, por el nombre que les da
  [lego](https://go-acme.github.io/lego/dns/).
- Para el correo: un proveedor que permita al servidor enviar por el puerto
  25. Muchos lo bloquean hasta que lo pides.

## Instalación

```bash
brew install eugene-panin/tap/damstack
damstack
```

La primera ejecución revisa tu Mac y dice qué falta y cómo conseguirlo.
damstack descarga una sola vez las herramientas con las que trabaja,
OpenTofu, Ansible y restic, y las mantiene aparte de todo lo demás en tu Mac.
No hace falta Docker.

## Tu primer servidor

```bash
damstack deploy hashi
```

damstack pregunta la dirección del servidor, el dominio de las páginas de
administración (por ejemplo `admin.example.com`), tu email para Let's
Encrypt, el token de Cloudflare y los dispositivos que pueden acceder al
servidor, como `laptop` y `phone`. Después, una sola vez, la contraseña de
root del servidor, para colocar allí tu clave SSH; no se guarda.

Luego ejecuta, en unos diez minutos:

1. **Proteger el servidor**: un usuario propio, acceso solo con tu clave,
   root y contraseñas desactivados, un cortafuegos y actualizaciones de
   seguridad automáticas.
2. **La plataforma**: la red WireGuard, Consul, Vault y Nomad, Docker, un
   reloj sincronizado y la copia de seguridad nocturna.
3. **Traefik y las páginas de administración**: un certificado para
   `*.<tu dominio>` y páginas que solo se abren a través de WireGuard.
4. **Registros DNS**, publicados automáticamente.

A mitad de camino se detiene para unir tu Mac a la red privada:

```bash
damstack tunnel show laptop
```

pone la configuración en la app de WireGuard; en un teléfono,
`damstack tunnel show phone --qr` muestra un código para escanear.

Al final muestra las páginas de administración, `nomad.`, `vault.` y
`consul.<tu dominio>`, y cómo obtener sus tokens:

```bash
damstack token nomad
```

`damstack trust` hace que tu Mac confíe en los certificados de la red
privada, para que los servidores también se abran en sus propias direcciones.

## Aplicaciones

```bash
damstack app add mail
damstack deploy
```

pregunta lo que necesita la aplicación y la ejecuta en tu servidor. La
biblioteca tiene **mail**, tu propio servidor de correo con
[Stalwart](https://stalw.art) (buzones para cada dominio, DKIM, MTA-STS, IMAP y
SMTP con certificados, y los registros DNS con los que los clientes de correo
encuentran su configuración), **odoo** y los servicios compartidos sobre los
que se construyen las aplicaciones: **postgres**, **nats**, **seaweedfs**
(S3), **loki**, **mimir** y **grafana**. Una aplicación de un repositorio
propio, público o privado, se añade igual.

Las aplicaciones se ejecutan en contenedores sobre Nomad, cada una solo con
los permisos que necesita, y pueden unirse a la service mesh de Consul: una
base de datos deja entrar entonces, por mTLS, solo a las aplicaciones
nombradas en su configuración.

Las claves de una aplicación, como las claves de API de un LLM, son sus
secretos: se piden al añadirla (una opcional puede quedar sin poner), se
cambian con `damstack secret set`, se guardan cifradas y llegan por Vault solo
a los servicios que las usan. Cambiar una clave reinicia solo esos servicios.

## En una terminal

`damstack deploy` muestra una vista a pantalla completa: cada paso, las tareas
del que está en marcha y cuántas quedan, el plan que hay que aprobar antes de
que nada cambie, lo que falló con las palabras de la herramienta que falló, y
al final cada dirección que sirve tu servidor, comprobada. Cuando Nomad
cambiaría un servicio de un modo que solo toma tras reiniciarlo, el plan lo
dice y ofrece reiniciarlo. `--plain`, una tubería o CI mantienen las líneas
simples.

## En equipo

Una vez arriba, el estado del proyecto vive en su servidor, no en un portátil:
cualquiera que tenga el proyecto puede desplegarlo. Los cambios toman un
bloqueo: un segundo despliegue espera su turno y muestra a quién espera.
`--break-glass` despliega sin el bloqueo, con el estado de tu Mac, solo para
reparar el propio servidor.

## Día a día

| Quieres | Ejecuta |
| --- | --- |
| Ver los proyectos y lo que te necesita, y hacer el resto desde ahí | `damstack` |
| Comprobar que el servidor está como lo configuraste | `damstack status --live` |
| Cambiar un ajuste, en una pantalla de secciones y campos (`--yaml` para el archivo) | `damstack edit` |
| Desplegar solo una parte, como una aplicación | `damstack deploy --only sauron` |
| Desplegar de nuevo, cambiando solo lo que difiere | `damstack deploy` |
| Desplegar solo las aplicaciones, sin tocar el servidor | `damstack deploy --apps-only` |
| Dar una clave a una aplicación, o ver cuáles hay | `damstack app keys sauron` |
| Ver todos los secretos, los de más riesgo primero, y cambiar los elegidos | `damstack secret` |
| Añadir o quitar un dispositivo de la red privada | `damstack tunnel add phone`, `damstack tunnel remove phone` |
| Dar claves nuevas a un dispositivo o al servidor | `damstack tunnel rotate phone` |
| Abrir una shell en el servidor, o ejecutar un comando allí | `damstack ssh`, `damstack ssh -- uptime` |
| Ver todo lo que damstack ejecutó | `damstack history` |
| Renombrar un proyecto en tu Mac | `damstack rename old new` |

Con varios proyectos, nombra el que quieres, como en `damstack status ovh`, o
haz que uno sea el actual con `damstack use ovh`.

## Copias de seguridad

El servidor hace una copia cada noche: los datos de Consul, Vault y Nomad, y
los de cada aplicación. damstack copia las copias de seguridad a tu Mac cada
hora mientras está encendido; `damstack backup status` las muestra, y la
pantalla de inicio avisa cuando se quedan viejas.

Todo lo que permite recuperar un proyecto vive en tu Mac. Crea un **kit de
recuperación**, un único archivo cifrado con el proyecto y su contraseña, y
guárdalo en otro lugar, como un almacenamiento en la nube, con su frase de
paso en tu gestor de contraseñas:

```bash
damstack backup kit
```

En otro Mac, `damstack backup open <kit>` recupera el proyecto.

## Si pierdes el servidor

Alquila un servidor nuevo, indica su dirección en el proyecto y restaura:

```bash
damstack edit          # la nueva dirección en server
damstack restore
```

damstack configura el servidor nuevo como hizo con el primero, y después
devuelve Consul, Vault y los datos de cada aplicación desde la última copia
de seguridad en tu Mac. Tus claves, tokens, buzones y correos quedan como
estaban.

## Dónde está cada cosa

Un proyecto es un directorio, `~/.damstack/<nombre>`:

- `stack.yaml`, el único archivo que se edita; `damstack edit` lo abre y lo
  comprueba;
- `vault.yml`, los secretos, cifrados; su contraseña está en
  `~/.config/damstack/projects/<nombre>`, fuera del proyecto;
- el estado de la plataforma y el historial de lo que se ejecutó.

Para quitar damstack: detén la copia horaria de cada proyecto con
`launchctl bootout gui/$(id -u)/dev.damstack.backup.<nombre>` y borra
`~/Library/LaunchAgents/dev.damstack.backup.<nombre>.plist`; luego
`brew uninstall damstack`, y borra `~/.damstack`, `~/.config/damstack` y
`~/.cache/damstack`. Conserva los dos primeros, o un kit de recuperación,
mientras funcione algún servidor tuyo: sin ellos nada puede gestionarlo.

## Límites actuales

- Solo macOS, y un servidor por proyecto.
- La vista a pantalla completa cubre la pantalla de inicio, los despliegues, los
  ajustes, el catálogo, las instalaciones, las aplicaciones, los enlaces, las
  claves y los secretos; las demás pantallas de su diseño llegan en próximas
  versiones.
- Las nuevas versiones de una plataforma o una aplicación se instalan a mano;
  todavía no existe `damstack upgrade`.
- Probado en OVH y Google Cloud.

## Más

- [docs/stacks.md](docs/stacks.md) (en inglés): cómo se escribe un stack y
  cómo desarrollar damstack.
- [docs/design.md](docs/design.md) (en inglés): hacia dónde va damstack.
- La plataforma: [damstack-hashi](https://github.com/eugene-panin/damstack-hashi);
  el correo: [damstack-mail](https://github.com/eugene-panin/damstack-mail).

Licencia MIT.

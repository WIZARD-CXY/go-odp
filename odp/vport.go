package odp

import (
	"fmt"
	"syscall"
)

type VportSpec interface {
	TypeName() string
	Name() string
	typeId() uint32
	optionNlAttrs(req *NlMsgBuilder)
}

type VportSpecBase struct {
	name string
}

func (v VportSpecBase) Name() string {
	return v.name
}

type SimpleVportSpec struct {
	VportSpecBase
	typ      uint32
	typeName string
}

func (s SimpleVportSpec) TypeName() string {
	return s.typeName
}

func (s SimpleVportSpec) typeId() uint32 {
	return s.typ
}

func (SimpleVportSpec) optionNlAttrs(req *NlMsgBuilder) {
}

func NewNetdevVportSpec(name string) VportSpec {
	return SimpleVportSpec{
		VportSpecBase{name},
		OVS_VPORT_TYPE_NETDEV,
		"netdev",
	}
}

func NewInternalVportSpec(name string) VportSpec {
	return SimpleVportSpec{
		VportSpecBase{name},
		OVS_VPORT_TYPE_INTERNAL,
		"internal",
	}
}

type VxlanVportSpec struct {
	VportSpecBase
	Port uint16
}

func (VxlanVportSpec) TypeName() string {
	return "vxlan"
}

func (VxlanVportSpec) typeId() uint32 {
	return OVS_VPORT_TYPE_VXLAN
}

func (v VxlanVportSpec) optionNlAttrs(req *NlMsgBuilder) {
	req.PutUint16Attr(OVS_TUNNEL_ATTR_DST_PORT, v.Port)
}

func NewVxlanVportSpec(name string, port uint16) VportSpec {
	return VxlanVportSpec{VportSpecBase{name}, port}
}

func parseVxlanVportSpec(name string, opts Attrs) (VportSpec, error) {
	port, err := opts.GetUint16(OVS_TUNNEL_ATTR_DST_PORT)
	if err != nil {
		return nil, err
	}

	return VxlanVportSpec{VportSpecBase{name}, port}, nil
}

// Vport numbers are scoped to a particular datapath
type VportID uint32

func parseVport(msg *NlMsgParser) (id VportID, s VportSpec, err error) {
	attrs, err := msg.TakeAttrs()
	if err != nil {
		return
	}

	rawid, err := attrs.GetUint32(OVS_VPORT_ATTR_PORT_NO)
	if err != nil {
		return
	}

	id = VportID(rawid)

	typ, err := attrs.GetUint32(OVS_VPORT_ATTR_TYPE)
	if err != nil {
		return
	}

	name, err := attrs.GetString(OVS_VPORT_ATTR_NAME)
	if err != nil {
		return
	}

	opts, err := attrs.GetNestedAttrs(OVS_VPORT_ATTR_OPTIONS, true)
	if err != nil {
		return
	}
	if opts == nil {
		opts = make(Attrs)
	}

	switch typ {
	case OVS_VPORT_TYPE_NETDEV:
		s = NewNetdevVportSpec(name)
		break

	case OVS_VPORT_TYPE_INTERNAL:
		s = NewInternalVportSpec(name)
		break

	case OVS_VPORT_TYPE_VXLAN:
		s, err = parseVxlanVportSpec(name, opts)
		break

	default:
		err = fmt.Errorf("unsupported vport type %d", typ)
	}

	return
}

func (dp DatapathHandle) CreateVport(spec VportSpec) (VportID, error) {
	dpif := dp.dpif

	req := NewNlMsgBuilder(RequestFlags, dpif.families[VPORT].id)
	req.PutGenlMsghdr(OVS_VPORT_CMD_NEW, OVS_VPORT_VERSION)
	req.putOvsHeader(dp.ifindex)
	req.PutStringAttr(OVS_VPORT_ATTR_NAME, spec.Name())
	req.PutUint32Attr(OVS_VPORT_ATTR_TYPE, spec.typeId())
	req.PutNestedAttrs(OVS_VPORT_ATTR_OPTIONS, func() {
		spec.optionNlAttrs(req)
	})
	req.PutUint32Attr(OVS_VPORT_ATTR_UPCALL_PID, 0)

	resp, err := dpif.sock.Request(req)
	if err != nil {
		return 0, err
	}

	_, _, err = dpif.checkNlMsgHeaders(resp, VPORT, OVS_VPORT_CMD_NEW)
	if err != nil {
		return 0, err
	}

	id, _, err := parseVport(resp)
	if err != nil {
		return 0, err
	}

	return id, nil
}

func IsNoSuchVportError(err error) bool {
	return err == NetlinkError(syscall.ENODEV)
}

type Vport struct {
	ID   VportID
	Spec VportSpec
}

func lookupVport(dpif *Dpif, dpifindex int32, name string) (int32, Vport, error) {
	req := NewNlMsgBuilder(RequestFlags, dpif.families[VPORT].id)
	req.PutGenlMsghdr(OVS_VPORT_CMD_GET, OVS_VPORT_VERSION)
	req.putOvsHeader(dpifindex)
	req.PutStringAttr(OVS_VPORT_ATTR_NAME, name)

	resp, err := dpif.sock.Request(req)
	if err != nil {
		return 0, Vport{}, err
	}

	_, ovshdr, err := dpif.checkNlMsgHeaders(resp, VPORT, OVS_VPORT_CMD_NEW)
	if err != nil {
		return 0, Vport{}, err
	}

	id, s, err := parseVport(resp)
	if err != nil {
		return 0, Vport{}, err
	}

	return ovshdr.DpIfIndex, Vport{id, s}, nil
}

func (dpif *Dpif) LookupVportByName(name string) (DatapathHandle, Vport, error) {
	dpifindex, vport, err := lookupVport(dpif, 0, name)
	return DatapathHandle{dpif: dpif, ifindex: dpifindex}, vport, err
}

func (dp DatapathHandle) LookupVportByName(name string) (Vport, error) {
	_, vport, err := lookupVport(dp.dpif, dp.ifindex, name)
	return vport, err
}

func (dp DatapathHandle) LookupVport(id VportID) (Vport, error) {
	req := NewNlMsgBuilder(RequestFlags, dp.dpif.families[VPORT].id)
	req.PutGenlMsghdr(OVS_VPORT_CMD_GET, OVS_VPORT_VERSION)
	req.putOvsHeader(dp.ifindex)
	req.PutUint32Attr(OVS_VPORT_ATTR_PORT_NO, uint32(id))

	resp, err := dp.dpif.sock.Request(req)
	if err != nil {
		return Vport{}, err
	}

	err = dp.checkNlMsgHeaders(resp, VPORT, OVS_VPORT_CMD_NEW)
	if err != nil {
		return Vport{}, err
	}

	id, s, err := parseVport(resp)
	if err != nil {
		return Vport{}, err
	}

	return Vport{id, s}, nil
}

func (dp DatapathHandle) LookupVportName(id VportID) (string, error) {
	vport, err := dp.LookupVport(id)
	if err != nil {
		if !IsNoSuchVportError(err) {
			return "", err
		}

		// No vport with the given port number, so just
		// show the number
		return fmt.Sprintf("%d:%d", dp.ifindex, id), nil
	}

	return vport.Spec.Name(), nil
}

func (dp DatapathHandle) EnumerateVports() ([]Vport, error) {
	req := NewNlMsgBuilder(DumpFlags, dp.dpif.families[VPORT].id)
	req.PutGenlMsghdr(OVS_VPORT_CMD_GET, OVS_VPORT_VERSION)
	req.putOvsHeader(dp.ifindex)

	var res []Vport
	consumer := func(resp *NlMsgParser) error {
		err := dp.checkNlMsgHeaders(resp, VPORT, OVS_VPORT_CMD_NEW)
		if err != nil {
			return err
		}

		id, spec, err := parseVport(resp)
		if err != nil {
			return err
		}

		res = append(res, Vport{id, spec})
		return nil
	}

	err := dp.dpif.sock.RequestMulti(req, consumer)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (dp DatapathHandle) DeleteVport(id VportID) error {
	req := NewNlMsgBuilder(RequestFlags, dp.dpif.families[VPORT].id)
	req.PutGenlMsghdr(OVS_VPORT_CMD_DEL, OVS_VPORT_VERSION)
	req.putOvsHeader(dp.ifindex)
	req.PutUint32Attr(OVS_VPORT_ATTR_PORT_NO, uint32(id))

	_, err := dp.dpif.sock.Request(req)
	return err
}

func (dp DatapathHandle) setVportUpcallPortId(id VportID, pid uint32) error {
	req := NewNlMsgBuilder(RequestFlags, dp.dpif.families[VPORT].id)
	req.PutGenlMsghdr(OVS_VPORT_CMD_SET, OVS_VPORT_VERSION)
	req.putOvsHeader(dp.ifindex)
	req.PutUint32Attr(OVS_VPORT_ATTR_PORT_NO, uint32(id))
	req.PutUint32Attr(OVS_VPORT_ATTR_UPCALL_PID, pid)

	_, err := dp.dpif.sock.Request(req)
	return err
}

type VportEventsConsumer interface {
	New(ifindex int32, vport Vport) error
	Delete(ifindex int32, vport Vport) error
	Error(err error, stopped bool)
}

func (dpif *Dpif) ConsumeVportEvents(consumer VportEventsConsumer) error {
	sock, err := OpenNetlinkSocket(syscall.NETLINK_GENERIC)
	if err != nil {
		return err
	}

	mcGroup, err := dpif.getMCGroup(VPORT, "ovs_vport")
	if err != nil {
		return err
	}

	go consumeVportEvents(dpif, sock, consumer)
	return syscall.SetsockoptInt(sock.fd, SOL_NETLINK, syscall.NETLINK_ADD_MEMBERSHIP, int(mcGroup))
}

func consumeVportEvents(dpif *Dpif, sock *NetlinkSocket, consumer VportEventsConsumer) {
	defer sock.Close()
	sock.consume(consumer, func(msg *NlMsgParser) error {
		genlhdr, ovshdr, err := dpif.checkNlMsgHeaders(msg, VPORT, -1)
		if err != nil {
			return err
		}

		id, spec, err := parseVport(msg)
		if err != nil {
			return err
		}

		switch genlhdr.Cmd {
		case OVS_VPORT_CMD_NEW:
			return consumer.New(ovshdr.DpIfIndex, Vport{id, spec})

		case OVS_VPORT_CMD_DEL:
			return consumer.Delete(ovshdr.DpIfIndex, Vport{id, spec})

		default:
			return nil
		}
	})
}

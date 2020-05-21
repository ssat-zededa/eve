# Generated by the protocol buffer compiler.  DO NOT EDIT!
# source: evecommon/devmodelcommon.proto

import sys
_b=sys.version_info[0]<3 and (lambda x:x) or (lambda x:x.encode('latin1'))
from google.protobuf.internal import enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from google.protobuf import reflection as _reflection
from google.protobuf import symbol_database as _symbol_database
# @@protoc_insertion_point(imports)

_sym_db = _symbol_database.Default()




DESCRIPTOR = _descriptor.FileDescriptor(
  name='evecommon/devmodelcommon.proto',
  package='org.lfedge.eve.common',
  syntax='proto3',
  serialized_options=_b('\n\025org.lfedge.eve.commonB\016DevModelCommonP\001Z\'github.com/lf-edge/eve/api/go/evecommon'),
  serialized_pb=_b('\n\x1e\x65vecommon/devmodelcommon.proto\x12\x15org.lfedge.eve.common*\x9b\x01\n\tPhyIoType\x12\r\n\tPhyIoNoop\x10\x00\x12\x0f\n\x0bPhyIoNetEth\x10\x01\x12\x0c\n\x08PhyIoUSB\x10\x02\x12\x0c\n\x08PhyIoCOM\x10\x03\x12\x0e\n\nPhyIoAudio\x10\x04\x12\x10\n\x0cPhyIoNetWLAN\x10\x05\x12\x10\n\x0cPhyIoNetWWAN\x10\x06\x12\r\n\tPhyIoHDMI\x10\x07\x12\x0f\n\nPhyIoOther\x10\xff\x01*\xa0\x01\n\x10PhyIoMemberUsage\x12\x12\n\x0ePhyIoUsageNone\x10\x00\x12\x19\n\x15PhyIoUsageMgmtAndApps\x10\x01\x12\x14\n\x10PhyIoUsageShared\x10\x02\x12\x17\n\x13PhyIoUsageDedicated\x10\x03\x12\x16\n\x12PhyIoUsageDisabled\x10\x04\x12\x16\n\x12PhyIoUsageMgmtOnly\x10\x05\x42R\n\x15org.lfedge.eve.commonB\x0e\x44\x65vModelCommonP\x01Z\'github.com/lf-edge/eve/api/go/evecommonb\x06proto3')
)

_PHYIOTYPE = _descriptor.EnumDescriptor(
  name='PhyIoType',
  full_name='org.lfedge.eve.common.PhyIoType',
  filename=None,
  file=DESCRIPTOR,
  values=[
    _descriptor.EnumValueDescriptor(
      name='PhyIoNoop', index=0, number=0,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoNetEth', index=1, number=1,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoUSB', index=2, number=2,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoCOM', index=3, number=3,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoAudio', index=4, number=4,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoNetWLAN', index=5, number=5,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoNetWWAN', index=6, number=6,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoHDMI', index=7, number=7,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoOther', index=8, number=255,
      serialized_options=None,
      type=None),
  ],
  containing_type=None,
  serialized_options=None,
  serialized_start=58,
  serialized_end=213,
)
_sym_db.RegisterEnumDescriptor(_PHYIOTYPE)

PhyIoType = enum_type_wrapper.EnumTypeWrapper(_PHYIOTYPE)
_PHYIOMEMBERUSAGE = _descriptor.EnumDescriptor(
  name='PhyIoMemberUsage',
  full_name='org.lfedge.eve.common.PhyIoMemberUsage',
  filename=None,
  file=DESCRIPTOR,
  values=[
    _descriptor.EnumValueDescriptor(
      name='PhyIoUsageNone', index=0, number=0,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoUsageMgmtAndApps', index=1, number=1,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoUsageShared', index=2, number=2,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoUsageDedicated', index=3, number=3,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoUsageDisabled', index=4, number=4,
      serialized_options=None,
      type=None),
    _descriptor.EnumValueDescriptor(
      name='PhyIoUsageMgmtOnly', index=5, number=5,
      serialized_options=None,
      type=None),
  ],
  containing_type=None,
  serialized_options=None,
  serialized_start=216,
  serialized_end=376,
)
_sym_db.RegisterEnumDescriptor(_PHYIOMEMBERUSAGE)

PhyIoMemberUsage = enum_type_wrapper.EnumTypeWrapper(_PHYIOMEMBERUSAGE)
PhyIoNoop = 0
PhyIoNetEth = 1
PhyIoUSB = 2
PhyIoCOM = 3
PhyIoAudio = 4
PhyIoNetWLAN = 5
PhyIoNetWWAN = 6
PhyIoHDMI = 7
PhyIoOther = 255
PhyIoUsageNone = 0
PhyIoUsageMgmtAndApps = 1
PhyIoUsageShared = 2
PhyIoUsageDedicated = 3
PhyIoUsageDisabled = 4
PhyIoUsageMgmtOnly = 5


DESCRIPTOR.enum_types_by_name['PhyIoType'] = _PHYIOTYPE
DESCRIPTOR.enum_types_by_name['PhyIoMemberUsage'] = _PHYIOMEMBERUSAGE
_sym_db.RegisterFileDescriptor(DESCRIPTOR)


DESCRIPTOR._options = None
# @@protoc_insertion_point(module_scope)
